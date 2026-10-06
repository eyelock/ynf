# See ynf in OpenTelemetry

Find where ynf writes its traces, logs and metrics, and follow one step, or one item's history, in
them. ynf writes OpenTelemetry on [ynr](https://github.com/eyelock/ynr)'s contract (ADR-011). It is
optional: with nothing set, ynf writes none, and its output and exit codes are what they were.

## Where ynf writes

ynf chooses once, at process start, in this order:

1. **The operator's endpoint**, if any `OTEL_EXPORTER_OTLP_*` variable is set: traces, logs and
   metrics go there over OTLP/HTTP, with the endpoint, headers and timeouts the standard variables
   give. This wins over everything below, except in a factory job with the collector on, where the
   spool wins (see [A factory job with the collector on](#a-factory-job-with-the-collector-on)).
2. **The spool root in ynf's configuration**, if `telemetry.spool` is set: ynf writes into the
   root's `factory/` folder. This is the layout a factory job uses, below.
3. **The spool**, if `YNR_SPOOL` names a folder, or the laptop default
   `$XDG_STATE_HOME/ynr/spool/local` exists (`~/.local/state/ynr/spool/local` when
   `XDG_STATE_HOME` is not set). ynf writes OTLP JSON lines into the folder named, in files called
   `ynf-<instance id>-<n>.jsonl`, and `ynr serve` reads them. `YNR_SPOOL` need not exist yet. On a
   laptop, `ynr serve` creates the default folder on its first start.
4. **Nothing.** The SDK's no-op providers: nothing is written.

`ynf serve`, which runs for as long as you leave it, looks for a spool again once a minute when it
found none, so one started after ynf is picked up. `OTEL_SDK_DISABLED=true` turns telemetry off.

Every record says `service.name=ynf`, `service.version` (ynf's version) and a
`service.instance.id`, a fresh ULID for each process. `OTEL_RESOURCE_ATTRIBUTES` adds to them. If
`TRACEPARENT` (and `TRACESTATE`) is in the environment, ynf's spans join that trace; otherwise
each step starts its own. Every process ynf starts, and every HTTP call to ynm, carries the trace
context on.

ynf's `slog` log is bridged into OpenTelemetry logs. What you read on stderr and in `--log-file`
does not change. What reaches telemetry leaves out the keys that carry content or people (a run's
detail, a decision's reason, who paused a lane), and secrets and email addresses are scrubbed from
the rest. People appear only as host-qualified handles, such as `github.com/octocat`. No prompt,
ticket text, code, diff or memory body is exported.

## What a step looks like

A step is one trace. Its root is the `ynf.step` span, carrying the item key
(`ynf.item.key`), `ynf.step.id`, the lane (`ynf.lane`, such as
`github.com/acme/factory-config#lint-paydown`), `ynf.policy.hash`, `ynf.lease.epoch` and the
repository (`ynf.repo`, `github.com/eyelock/ynh`). Beneath it:

- `ynf.claim`, taking the item's lease
- for each decision: `ynf.probe`, `ynf.decide` and one `ynf.act` for each action; a run is
  `ynf.run` under its `ynf.act`
- `ynf.call` for each call out to another system (forge, tracker, git, ynm, the executor, ynh
  building an image), with `ynf.call.system` and `ynf.call.operation`

Each unit ends with ynf's own outcome as `ynf.outcome` (a run's is `converged`, `budget`, `stuck`
and the rest) and its span status follows it. `ynf.step.started` and `ynf.run.started` are written
when each begins, so a crash shows as a start with no finish.

An item's history is linked, not nested. Each CloudEvent ynf receives is a short `ynf.intake` span
holding exactly one `ynf.intake.received` event, with the standard `cloudevents.event_id`,
`event_source`, `event_type` and `event_subject` and whether it was accepted, deduplicated or
rejected. Each step span links to the item's previous step and to the intake that started it. ynf
keeps the last step's trace and span ids on the item (`ynf items show <item>`, under `trace`), so
the links hold across processes and on every store. Nothing in ynf reads telemetry: CloudEvents are
still the control plane.

Metrics are `ynf.run.count` (by outcome, lane and model), `ynf.run.tokens` and `ynf.run.cost` (by
model) and `ynf.lease.expired` (by lane). Their attributes are low-cardinality; the limits are in
the registry.

## Look at a step

With a spool, read the files directly. Each line is one OTLP request:

```bash
export YNR_SPOOL=/tmp/ynf-spool
ynf start github.com/acme/payments#12
jq -c '.resourceSpans[]?.scopeSpans[].spans[] | {name, spanId, links: [.links[]?.spanId]}' "$YNR_SPOOL"/ynf-*.jsonl
```

Find an item's steps by `ynf.item.key`, and read them in order by following each step's links.
With `ynr serve` running, its dashboards do this. With an operator's endpoint, query it for
`ynf.item.key`.

## A factory job with the collector on

A job that runs the factory, `ynf sweep` or `ynf serve` in a container or on a runner, can collect
all of its telemetry, the runs' included, with `ynr serve`. Configure it, and ynf starts `ynr
serve` for the job and stops it at the end:

```yaml
# config.yaml
telemetry:
  spool: /var/spool/ynf       # its own filesystem, such as a tmpfs
  run_quota: 64MiB
  collector:
    enabled: true
    id: gha-linux-pool        # the runner pool, not this job
    upstream: https://otel.example.com
```

The spool root then holds three things:

- `factory/`, which ynf writes its own telemetry to;
- `runs/<run id>/`, one folder for each run, which the run writes into as `YNR_SPOOL` (in docker mode
  it is the only part of the spool the container has, at `/run/ynr/spool`, so the run needs no
  network path to a collector);
- `manifests/<run id>.json`, one for each run, which the run cannot reach. It names the run's lane,
  harness, focus, item and step, and `ynr serve` stamps those on everything from the run's folder
  (`ynf.lane`, `ynf.lane.harness`, `ynf.lane.focus`, `ynf.item.key`, `ynf.step.id`, `ynf.run.id`),
  with `ynr.provenance=run`. A run cannot claim another lane.

`TRACEPARENT` is set for each run, so what the run writes is in the step's trace. A lane with
`run.ynh.telemetry_relay: true` also sets `YNH_TELEMETRY_RELAY=1`, so the vendor CLI's own
telemetry is relayed into the run's folder.

With the collector on, ynf and the runs write to the spool and not to the operator's endpoint, and
`ynr serve` ships to the endpoint instead: set `OTEL_EXPORTER_OTLP_ENDPOINT` as usual, or
`telemetry.collector.upstream`. `ynr serve` needs one of them until it has an object store, and
the config does not load without. At the end of the job ynf stops `ynr serve` with `SIGTERM`, gives
it `collector.archive` (30 seconds) to ship, and copies any `*.jsonl` or `*.open.jsonl` still in
`runs/` or `factory/` into the run capture: a run's own into `<work_dir>/steps/<item>/<run>/spool/`,
the rest into `<work_dir>/spool-capture/<time>/`.

`ynr` has to be there, and nothing starts it because it is: `ynf doctor` shows whether it was
found. If it is missing or does not start, the job says so in its log and runs on, with its
telemetry left in the spool.

A run that fills its folder has the largest files removed, within a quarter of a second, and
ynf says so in its log; the step goes on. [ADR-007](../adr/007-executor-and-containment.md) says what
that does and does not guarantee on each executor and host. `make -C sandbox e2e` proves all of
this against the sandbox when `ynr` is on `PATH`: it runs the sweep with the collector on, with a receiver of its own as the
upstream, and checks each run's manifest and folder, that the run's own records arrived with
`ynr.provenance=run` and the lane the manifest names, and that a flooded folder was held to its
quota.

## The names

`ynf telemetry registry --format json` prints every name ynf emits, as an OpenTelemetry Weaver
registry pinned to a semantic-conventions release. It lives in `telemetry/registry` in the
repository, and CI checks it with Weaver.
