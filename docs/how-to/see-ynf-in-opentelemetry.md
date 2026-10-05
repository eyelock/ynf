# See ynf in OpenTelemetry

Find where ynf writes its traces, logs and metrics, and follow one step, or one item's history, in
them. ynf writes OpenTelemetry on [ynr](https://github.com/eyelock/ynr)'s contract (ADR-011). It is
optional: with nothing set, ynf writes none, and its output and exit codes are what they were.

## Where ynf writes

ynf chooses once, at process start, in this order:

1. **The operator's endpoint**, if any `OTEL_EXPORTER_OTLP_*` variable is set: traces, logs and
   metrics go there over OTLP/HTTP, with the endpoint, headers and timeouts the standard variables
   give. This wins over everything below.
2. **The spool**, if `YNR_SPOOL` names a folder, or the laptop default
   `$XDG_STATE_HOME/ynr/spool/local` exists (`~/.local/state/ynr/spool/local` when
   `XDG_STATE_HOME` is not set). ynf writes OTLP JSON lines into the folder named, in files called
   `ynf-<instance id>-<n>.jsonl`, and `ynr serve` reads them. `YNR_SPOOL` need not exist yet. On a
   laptop, `ynr serve` creates the default folder on its first start.
3. **Nothing.** The SDK's no-op providers: nothing is written.

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

## The names

`ynf telemetry registry --format json` prints every name ynf emits, as an OpenTelemetry Weaver
registry pinned to a semantic-conventions release. It lives in `telemetry/registry` in the
repository, and CI checks it with Weaver.
