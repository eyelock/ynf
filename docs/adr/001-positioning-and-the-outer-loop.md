# ADR-001: Positioning and the outer loop

Status: draft (2026-10-03)
Satisfies: FR-8, FR-11, NFR-8

## Context

ynh's factory pattern stacks three layers. Acceptance (what "done" means in your repositories) is
yours. Harness (guides, focuses, profiles, sensors, the agent loop) is ynh. Runtime (model,
execution, sandbox, egress, credential scoping, queue, scheduling, audit log) is "the operator's:
vendor CLI, CI, or a container". ynh's docs say in several places that the loop belongs to "CI,
an orchestrator, a custom tool", and that ynh does not add attribution, open pull requests or
enforce containment.

`ynh agent run` is a loop already: plan, act, run `ynh check`, feed failures back, stop on
convergence, budget or stuck detection. It lasts minutes. The work around it lasts days: CI takes
twenty minutes, a reviewer replies tomorrow, a ticket is relabelled next week.

## Decision

**ynf is the runtime layer.** It owns the queue, scheduling, audit, containment and credential
scoping that ynh leaves to the operator, and nothing in the harness layer.

**A loop around a loop.** ynh runs the *inner* loop: one bounded run against sensors. ynf runs the
*outer* loop: a durable, event-driven state machine per work item, where each step is

```
event ─► correlate to a work item ─► claim ─► probe facts ─► decide ─► act ─► record ─► release
```

and the end of a run is itself an event that drives the next step.

**What ynf adds on top of `ynh agent run`:**

- deciding *whether* and *when* to run, and with which harness and focus (ADR-006)
- reading facts ynh cannot see from inside a run: the real CI result, reviews, ticket state
  (ADR-003)
- every forge and ticket write, including attribution trailers (ADR-007, ADR-010)
- exclusivity and recovery across processes and hosts (ADR-005)
- memory across runs and items (ADR-008)
- the factory numbers and stop conditions (ADR-010)

**ynh is the default inner loop, not a requirement.** ynf talks to its inner loop through a
runner port, and `ynh agent run` is one provider behind it, detected with no configuration when
installed (ADR-012).

**Integration is out of process** (NFR-8). With ynh, ynf runs `ynh agent run --format json
--emit-jsonl <path>` and `ynh check --format json`, and talks to ynm through `ynm … --json` or its MCP HTTP
endpoint. ynh's packages are `internal/`, and ynm's library is not published, so this is also the
only option; it is also the right boundary, since it keeps each tool's contract its CLI.

## Alternatives

- **Put the outer loop in ynh.** Rejected: ynh's design deliberately stops at the harness layer,
  and containment and credential scoping cannot be claimed by a tool that also runs the agent.
- **CI workflows only, no ynf.** Workable for one repository and one lane, but each team would
  rebuild correlation, exclusivity, retries and the stop conditions in YAML. ynf keeps CI as one
  of its hosts (ADR-009) instead.

## Consequences

- ynf's correctness depends on ynh's run result schema and exit codes. They are versioned
  contracts (`docs/schema/cli/agent-run.schema.json` in ynh); ynf pins a supported range and
  fails at start-up outside it.
- ynh's docs that say it "does not ship a loop driver" predate `ynh agent run` and need a pass so
  the boundary reads the same from both sides.

## Open questions

- Does ynh grow a `--trailer-file` or similar so ynf does not have to reconstruct commit metadata,
  or does ynf always own commits entirely? ADR-007 currently assumes the latter.

## History

- 2026-10-03: drafted.
