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
ynf serve        [--store <url>] [--executor docker] [--lanes <file>]
ynf step         --event <file|-> [--interactive]
ynf sweep        [--lane <name>]
ynf lanes        validate | ls | show <lane> | explain <lane> --item <key>
ynf items        ls [--lane] [--state] | show <key> | log <key>
ynf items        release <key> | retry <key> | quarantine <key>
ynf replay       <key> [--policy <file>] [--since <time>]
ynf pause        <lane> --reason <text>
ynf resume       <lane> --reason <text>
ynf stats        [--lane <name>] [--window 30d]
ynf shadow       <lane> --since 90d
ynf doctor
```

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
failure signatures with ynm's reflective summaries.

**Observability:** structured JSON logs, and an OpenTelemetry trace per step with spans for
claim, probe, decide, act, and the run. Every span carries the item key, `step_id`, lane, policy
hash and lease epoch. Exit codes for `ynf step`: `0` acted or nothing to do, `10` lost the claim,
`20` adapter error, `30` policy invalid, `40` containment unavailable.

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
