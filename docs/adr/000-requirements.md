# ADR-000: Requirements

Status: draft (2026-10-03)

## Context

ynf ("your named factory") is the third sibling. ynh ("your named harness") manages how an agent
is guided and runs a bounded, resumable agent loop with `ynh agent run`. ynm ("your named memory")
manages what agents remember. Neither owns what happens *between* runs: noticing that a ticket is
ready, starting a run, watching the pull request it led to, reacting to red CI or a review, and
deciding what the next turn should be. ynh's
[factory pattern](https://github.com/eyelock/ynh/blob/main/docs/factory-pattern.md) names that
layer explicitly, the runtime layer ("queue, scheduling, audit log"), and assigns it to the
operator. ynf is that operator.

These are the requirements the other ADRs cite by id. They follow the same draft, addenda,
consolidate lifecycle as the ADRs.

## Functional requirements

### Intake
- FR-1 Accept push events from forges and ticket systems: GitHub (issues, pull requests, check
  runs and suites, statuses, reviews, comments) and JIRA (issue created, updated, transitioned,
  labelled).
- FR-2 Consume events from message topics (SQS/SNS, EventBridge, NATS, Pub/Sub, Kafka) carrying
  the same envelope.
- FR-3 Run scheduled searches (JQL, GitHub search, others behind the same seam) that find
  eligible tickets and pull requests, and reconcile anything a push event missed.
- FR-4 Treat its own happenings as events: a run finished, a timer fell due, a lease expired.
- FR-5 Normalise every event to one envelope, deduplicate by delivery id, and keep the raw
  payload for audit.

### Work items
- FR-6 Track one work item per unit of work (a ticket, an issue or a pull request), correlating
  the ticket, its branch, its pull request and its runs to the same item.
- FR-7 Support items ynf **originated** (it opened the branch and pull request) and, in lanes that
  opt in, items it **adopted** (a pull request someone else opened).

### Decision
- FR-8 Decide the next action for an item deterministically from the triggering event, the
  item's stored state and freshly probed facts.
- FR-9 Express per-lane policy (eligibility, routing to a harness and focus, retry, escalation,
  stop conditions) declaratively, validated against a schema.
- FR-10 Any recorded decision can be replayed from its inputs under the same or a different
  policy.

### Execution
- FR-11 Run agent turns through a runner: `ynh agent run` when ynh is available, or any command,
  collecting the run result and trajectory.
- FR-12 Branch on ynf's own run outcomes (converged, budget, stuck, tamper, operator error,
  error, aborted), mapped from each runner's own signals.
- FR-13 Perform every forge and ticket write (commit, push, pull request, comment, label,
  transition) itself, never from inside the agent.
- FR-14 Steer a live run through ynh's stdin control channel (approve or reject a plan, replace
  feedback, interrupt).

### Exclusivity and fault tolerance
- FR-15 At most one ynf instance acts on a work item at a time, across every host and process.
- FR-16 Work held by a crashed or partitioned instance is picked up by another, without
  duplicated side effects.
- FR-17 Retries are capped; an item that exhausts them is quarantined and escalated.

### State
- FR-18 State lives behind a store port with SQLite, S3 and DynamoDB providers, chosen by
  configuration.

### Memory
- FR-19 Write each observed failure to ynm, keyed so recurring failures
  cluster.
- FR-20 Make what ynf learns available to the people and agents who need it: known failure
  patterns are readable in ynm, and an agent whose harness connects to the same store reads them
  itself. ynf never puts memory into an agent's task (ADR-008).

### Governance
- FR-21 Every commit ynf writes carries ynf's attribution trailers, plus `YNH-Session` when the
  runner is ynh.
- FR-22 Every run's result and trajectory are stored with the item, with a per-lane retention
  period.
- FR-23 Per-lane stop conditions (yield floor, review-time ceiling, escaped defects, rubber-stamp
  detector, queue divergence) pause a lane automatically.
- FR-24 Report the factory numbers per lane: yield `y`, review time `r`, attempts, top failure
  signatures.

### Operation
- FR-25 A CLI to run, inspect, release, retry, quarantine, pause and resume.
- FR-26 Shadow mode: measure a lane's yield against history before it proposes anything.

## Non-functional requirements

- NFR-1 One binary, four hosts: a developer's machine, a pool of always-on workers, a job runner, and a CI job.
- NFR-2 No in-memory state that matters. Any process can die between any two steps.
- NFR-3 Determinism: the decider is a pure function; model output never steers control flow.
- NFR-4 Unattended lanes never run uncontained.
- NFR-5 Ticket, issue and comment text is untrusted input. It never reaches anything that
  interprets it as policy, and reaches the agent only as data.
- NFR-6 Every external side effect is idempotent per step.
- NFR-7 Conventions mirror ynh: stable `--format json`, meaningful exit codes, every environment
  variable a fallback for an explicit flag.
- NFR-8 ynf imports no ynh or ynm code. It talks to them through their CLIs and protocols.
- NFR-9 Observable: structured logs and OpenTelemetry traces per step.
- NFR-10 Written in Go.
- NFR-11 Loosely coupled to ynh and ynm: neither is required, and when they are installed ynf
  detects and uses them with no configuration.

## History

- 2026-10-03: drafted.
