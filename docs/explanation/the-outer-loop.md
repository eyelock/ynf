# The outer loop

ynf runs a loop around a loop. This page explains what each loop is for, and why the outer one is
its own tool.

## The inner loop is ynh's

`ynh agent run` takes a task and a harness, plans, acts, runs `ynh check` against the harness's
sensors, feeds the failures back, and stops when the sensors converge, a budget runs out, or the
run is stuck. It returns one JSON object saying how it ended, and an exit code that means
something: `0` converged, `10` to `12` a budget, `13` stuck, `14` the baseline moved under it.

ynh is the inner loop ynf finds and uses with no configuration when it is installed, but not the
only one: any command can be a runner, and lanes branch on ynf's own outcomes rather than on
ynh's exit codes ([ADR-012](../adr/012-loose-coupling-and-detection.md)).

That loop is good at one thing: getting a change to pass a set of checks it can run itself, in
minutes.

## The work around it takes days

A real change does not end when the local sensors pass. CI runs, and takes twenty minutes, and
sometimes disagrees with the sensors. A reviewer asks for changes tomorrow. Someone relabels the
ticket next week. Each of those is a moment where somebody has to decide what happens next: try
again, try again with the reviewer's comments, give up and ask a human, or stop.

ynh deliberately does not make those decisions. Its factory pattern splits a factory into three
layers (acceptance, harness, runtime) and gives the runtime layer, "queue, scheduling, audit
log", to the operator. Its docs say the loop belongs to "CI, an orchestrator, a custom tool".

ynf is that orchestrator.

## One step at a time

The outer loop is a state machine per work item, and it moves one step at a time:

```
event ─► correlate ─► claim ─► probe ─► decide ─► act ─► record ─► release
```

An **event** arrives: a webhook, a message on a topic, a scheduled search, or ynf's own "the run
finished". ynf **correlates** it to a work item, **claims** the item so no other instance acts on
it, **probes** the current facts (the pull request's checks, its reviews, the ticket's labels),
**decides** what to do under the item's lane policy, **acts**, **records** everything it saw and
did, and **releases** the claim.

The end of a run is itself an event, so the loop keeps itself going.

## Why the decision is a pure function

`Decide` takes the lane policy, the item's state, the probed facts and the event, and returns the
next state and a list of actions. It does no I/O and calls no model. Three things follow:

- **Replay.** Every step records its inputs, so any decision can be recomputed, and a policy
  change can be tested against last month's events before it ships.
- **Hosts are interchangeable.** A step needs nothing in memory from the last one, so it can run
  in a daemon, a hosted service, or a single CI job that exits afterwards.
- **Models make changes, not control flow.** The agent proposes a diff. Whether that diff gets
  pushed, retried or escalated is decided by rules a person wrote and a reviewer approved.

See [ADR-001](../adr/001-positioning-and-the-outer-loop.md) and
[ADR-006](../adr/006-decider-and-lane-policy.md).
