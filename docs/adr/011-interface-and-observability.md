# ADR-011: Interface and observability

Status: draft (2026-10-03)
Satisfies: FR-10, FR-24, FR-25, FR-26, NFR-7, NFR-9

## Context

Operators need to see what ynf is doing, why it decided what it decided, and intervene on one
item without editing state by hand. Policy authors need to know what a change to a lane would do
before it ships.

## Decision

**The CLI:**

```
Work
ynf start        <ref> [--repo <host/org/repo>] [--lane <name>] [--auto-approve edits|all] [--detach]
ynf start        --prompt <text> --repo <host/org/repo> [--lane <name>] [--auto-approve …] [--detach]
ynf serve        [--interval 1m] [--listen :8080] [--lane <name>]...     also POST /start
ynf sweep        [--until-settled] [--timeout 20m] [--lane <name>]...
ynf handle       --github-event <file> --github-event-name <name>        an event, CI-native

Items
ynf items        ls [--tracker <name>] [--repo …] [--state …] | show | log | retry | release | quarantine <ref>
ynf replay       <ref> [--policy <file>]

Policy and governance
ynf lanes        validate [--file <path>] | show --repo <host/org/repo> [<lane>] | explain <lane> --item <ref>
ynf pause        <lane> --reason <text> [--repo …]
ynf resume       <lane> --reason <text> [--repo …]
ynf stats        [--lane <name>] [--window 30d]
ynf shadow       run <lane> [--since 90d] | ls | grade [<run>] | report [<run> | --lane <lane>]

Connections
ynf trackers     ls | get <ref>                                           get reads without starting
ynf forges       ls
ynf harness      show <image> | --lane <name> --repo …                    what ynf reads from the image

Operations
ynf version
ynf doctor
ynf telemetry    registry [--format json]                                 the names ynf emits in OpenTelemetry
ynf egress-proxy --allow <hosts> [--listen :3128] [--log <file>]          inside a container
```

Global flags come before the command: `--config <path>` (default: `config.yaml` found in the home
factory folder, ADR-009), `--format text|json`, `--interactive` (allows the uncontained `process`
executor, ADR-007), `--log-file <path>`, `--log-format text|json` and `-v`.

**References.** An item is named by its reference (ADR-002): `github.com/eyelock/ynh#77`,
`example.atlassian.net/PLAT-881`, `adhoc/<id>`, or by its store key. Shorthands are for typing only:
`eyelock/ynh#77` means the default GitHub instance, `jira/PLAT-881` the tracker configured as
`jira`. They resolve to the host form before anything is stored, so configured names never reach
an item's identity.

**Two verbs for work.** `ynf start` is an *instruction*: it names one item and says take it on now.
`ynf handle` is an *event*: something happened, and ynf works out which items it touches, which may
be several or none. Both feed the same `step` (ADR-009). `ynf start`:

- resolves the reference, reads the ticket through its tracker, checks where the code goes (ADR-002)
  and that the lane exists and accepts the item, and fails before creating anything if any check
  does
- counts as attended when run from a terminal, so it may use the `process` executor, and is the
  only way to switch off an agent's approval prompts outside containment (`--auto-approve`,
  ADR-007)
- steps the item now and returns when it is waiting on something outside ynf (CI, a review, a
  person), printing its state and any pull request; `--detach` only records it, for a running
  `ynf serve` to pick up

**Connections are inspectable.** `ynf trackers get <ref>` prints the structured ticket ynf would
read, and `ynf harness show` prints what ynf reads from an image (its harness, focuses, budgets,
sensors, passthrough variables and ynh capabilities), so a configuration can be checked without
starting work. `ynf doctor` also checks every tracker and forge is reachable, and that git, docker, ynh
and ynm are installed (ynh and ynm are optional).

Every command takes `--format json` and returns one object.

**Replay** re-runs `Decide` over an item's recorded `{event, facts}` entries under a different
policy and reports where the decisions differ. It is only possible because the decider is pure
and facts are recorded per step (ADR-006). It makes a policy change testable against last month's
traffic before it is merged.

**Shadow mode** is rung three of ynh's adoption ladder: run a lane against closed tickets whose
correct answer is known, propose nothing, and report the observed yield with its confidence
interval.

**Stats** computes ynh's factory numbers per lane: yield `y`, review time `r`, break-even
`y* = r / h` when the lane declares `h`, attempts per item, cost per merged change, and the top
failure signatures with ynm's reflective summaries. It breaks each lane down by **model and
effort** (which can do the work, at what cost): runs, how many converged, turns and tokens per
run, cost where the runner reports it, and the proposals, merges and rejections of the model whose
change was proposed. That answers "does this lane need a bigger model, or could it use a smaller
one or less effort", and replay then tests a cheaper policy against the recorded steps.

**Every run's record says what it ran on and spent:** the model, effort, turns, tokens, cost, the
cap that bound it, the harness's name, version and commit (a harness change is a confounder, not a
model effect), the runner's version and the approval level. It is read from the runner's result:
ynh's, or a command's result file. ynf records what the runner reports and never prices tokens
itself. These records live in ynf's own store, the run history; memory holds only failure
patterns (ADR-008).

**Observability** is ynf's side of ynr's contract for OpenTelemetry (ynr ADR-006), and works the
same with ynr or without it.

*Logs.* Structured logs as they happen, to stderr and with `--log-file` to a file, as text or one
JSON object per line: every decision, every run's start, progress and finish, and every action on
a tracker or forge. They are bridged into OpenTelemetry logs with the official `log/slog` bridge;
what people read is unchanged byte for byte, and what the bridge sends leaves out content and
people and scrubs secrets and email addresses.

*Where it goes.* The SDK is set up once at process start (`internal/telemetry`), and writes, in
order: to the operator's `OTEL_EXPORTER_OTLP_*` endpoint if set; else to `factory/` under the
spool root in ynf's configuration, if it names one (ADR-009); else to the spool, if `YNR_SPOOL`
names a folder or the laptop default `$XDG_STATE_HOME/ynr/spool/local` exists, through ynr's spool
exporter, into the folder named; else nowhere, with the no-op providers. In a factory job with the
collector on, the spool root comes first: ynf writes to the spool whatever the operator set, and the
operator's endpoint is `ynr serve`'s upstream instead (ynr ADR-004). `ynf serve` looks for a
spool again once a minute when it found none. The resource is `service.name=ynf`, ynf's version
and a ULID for each process, and `OTEL_RESOURCE_ATTRIBUTES` is honoured. ynf joins the trace in
`TRACEPARENT` and `TRACESTATE`, else starts one; every process it starts gets them, and every HTTP
call to ynm carries W3C trace-context headers. The flush at exit is bounded (ForceFlush, the
spool's Sync, Shutdown, Close), and errors are swallowed: telemetry never changes output or an
exit code.

*A trace per step.* The step's span is the root and carries the item key, `step_id`, lane id
(ADR-006), policy hash, lease epoch and the repository, host first. Beneath it are spans for claim,
probe, decide and act (one for each action), the run under its action, and a `ynf.call` span for
each call out to another system: the forge, a tracker, git, ynm, the executor, and ynh building an
image. `ynf.step.started` and `ynf.run.started` are written as each begins, so a crash shows as a
start with no finish. Each unit of work ends with ynf's own outcome as `ynf.outcome` and a span
status set from it.

*An item's history is linked, not nested.* Each CloudEvent received is an `ynf.intake` span with
its `ynf.intake.received` event (ADR-002). Each step span links to the item's previous step and to
the intake that started it, and ynf keeps the last step's span ids, and an intake no step has
linked to yet, on the item in its store, so this works across processes on every provider.
Nothing in ynf reads telemetry.

*The spool and the collector in a factory job.* With a spool root, ynf lays it out as ynr reads it
(ynr ADR-003): `factory/` for its own writer, `runs/<run id>/` for each run, and
`manifests/<run id>.json` for each run, which names the run's lane id, harness, focus, item and
step. Before a run starts, ynf makes its folder and writes its manifest (to a temporary name, then
renamed, never over a link), and starts the run with `YNR_SPOOL` set to its folder, `TRACEPARENT`
and `TRACESTATE`, and without the operator's `OTEL_EXPORTER_OTLP_*` when the collector is on.
ADR-007 says how a run is kept to its folder. A lane with `run.ynh.telemetry_relay` also sets
`YNH_TELEMETRY_RELAY=1` for its runs, so `ynh agent run` relays the vendor CLI's telemetry into
that folder (ynr ADR-004); command lanes refuse the setting, since they have no vendor. When the
configuration enables the collector, and only then, a factory job (`sweep`, `serve`, `handle` and
`shadow run`) starts `ynr serve --spool <root> --collector-id <id>`, with the instance and the
upstream when there are any, and stops it at the job's end with `SIGTERM`, giving it the archive time
to ship before what is left goes into the run capture (ADR-010). `ynf start`, a person's own
command, and commands that only read or record start nothing. If `ynr` is missing or fails, the job
says so in its log and in `ynf doctor` and runs on: telemetry never fails the factory. A job killed
without a chance to stop `ynr serve` leaves it running until its container ends. The spool and the
manifest are written, and the folder given, with the collector off too, whenever a root is
configured, for an operator's own `ynr serve`; the run's files are then copied into its capture
when it ends.

*Metrics.* Runs by outcome, lane and model; tokens and cost by model, as the runner reports them;
lease expiries by lane. Attributes are low-cardinality, with each limit declared in the registry,
and never an item key, run id or trace id.

*Names.* ynf's names are an OpenTelemetry Weaver registry in `telemetry/registry`, under the
`ynf.` prefix and pinned to a semantic-conventions release, including the factory attributes ynr
stamps on ynf's behalf (`ynf.lane`, `ynf.lane.harness`, `ynf.lane.focus`). It is embedded in the
binary, `ynf telemetry registry --format json` prints it, and the Go constants ynf uses are
generated from it. CI checks it with Weaver.

*Conformance.* `ynr conformance` (ynr ADR-008) runs in CI as the `conformance` job, against a
pinned ynr release whose binaries are checked against the release's checksums and put on `PATH`;
ynf never builds or installs ynr. Its scenarios are `.ynr/conformance.yaml`, each a `ynf sweep` or
`ynf start` against an offline stand-in for GitHub with its own config and store, so they need no
network, no clone, no docker and no model. They check where ynf writes, its resource, the trace it
joins, a started event for each step, outcomes and statuses that agree, its names against the
registry, that planted ticket text, file contents, a prompt and a token never reach a record, that a
full spool or an endpoint that never answers changes neither its exit code nor its time, and that it
starts nothing because `ynr` is on `PATH`. A kill mid-step leaves the started event in the spool.
The check through the whole chain, a ynf step into `ynh agent run` with the relay on and ynr's stub
vendor, belongs to the factory image's CI and waits for a ynh release with relay support.

*People and content.* An actor appears only as a host-qualified handle, such as
`github.com/octocat`, never a name or email. ynf's forge and tracker ports return structure and no
author: the facts a decision reads hold labels, states and check conclusions, and the review logins
the GitHub adapter reads are folded into whether a pull request is approved and discarded. No
record of ynf's names a person today, and `telemetry.Handle` is where one would be qualified by
host if a port ever returned one. No prompt, ticket text, code, diff or memory body is
exported, and secrets are redacted at the source.

**Exit codes**, for every command: `0` success, `2` usage, `20` an adapter failed (the forge, git,
docker, the store), `30` config or lanes invalid, `31` `sweep --until-settled` timed out before
every item settled, `32` `replay` found decisions that differ, `33` `start` refused the item (the
reference does not resolve, the tracker cannot read it, the repository is not enrolled, reachable
or in agreement with the ticket, or no lane accepts it). A step that loses its claim, or a
lane that cannot run contained, is not an exit code: the first is another instance's work, and the
second is recorded on the item as an `operator_error` outcome and escalated.

## Alternatives

- **A web UI first.** Later, if at all; the CLI and `--format json` come first so everything is
  scriptable.

## Consequences

- The tutorials will double as acceptance tests, as ynm's do: a model runs them and checks the
  output.

## Open questions

- Is a read-only status page in the hosted service worth having early?

## History

- 2026-10-03: drafted.
