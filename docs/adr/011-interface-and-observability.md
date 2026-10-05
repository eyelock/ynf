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
ynf egress-proxy --allow <hosts> [--listen :3128] [--log <file>]          inside a container
```

Global flags come before the command: `--config <path>` (default: `config.yaml` found in the home
factory folder, ADR-009), `--format text|json`, `--interactive` (allows the uncontained `process`
executor, ADR-007), `--log-file <path>`, `--log-format text|json` and `-v`.

**References.** An item is named by its reference (ADR-002): `github.com/eyelock/ynh#77`,
`acme.atlassian.net/PLAT-881`, `adhoc/<id>`, or by its store key. Shorthands are for typing only:
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

**Observability:** structured logs as they happen, to stderr and with `--log-file` to a file, as
text or one JSON object per line: every decision, every run's start, progress and finish, and every
action on a tracker or forge. An OpenTelemetry trace per step has spans for claim, probe, decide,
act, and the run. Every span carries the item key, `step_id`, lane, policy
hash and lease epoch.

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
