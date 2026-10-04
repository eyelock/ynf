# Learning from failure

ynf sees every run's outcome across every work item. This page explains how it turns recurring
failures into memory in ynm, and why that memory informs people and agents but never decides
anything.

## What only ynf can see

A ynh run knows its own trajectory. It does not know that the last four runs on this repository
were stuck on the same test, or that its sensors keep passing while the real CI fails. ynf does,
because it records the outcome of every step: the exit code, which budget bound the run, which
sensors failed, what CI said, what the reviewer said.

The most useful pattern is one no single run can see: **converged locally, failed in CI.** It means
the harness's sensors and the real gate disagree, which is drift ynh's `version_command` and
`--calibrate` exist to catch.

## Two places: the history and the patterns

ynf keeps the whole history itself. Every decision, run and action is in the item's log in ynf's
own store, with each run's model, effort, turns, tokens and cost: that is what `ynf replay` reads
and what `ynf stats` breaks down by model. ynm gets only the patterns. Its consolidation is built
to merge memories it judges to say the same thing and to retire ones a newer memory supersedes,
which is right for knowledge and wrong for a history: "attempt 1 failed" and "attempt 2
converged" would be pruned into something that never happened. So steps stay in ynf's store, and
ynm holds failure occurrences.

## Signatures

For each occurrence of a failure, ynf writes an episodic memory to ynm whose subject is a
**failure signature**: a short, normalised, deterministic key. Each occurrence's text names its
item, run, step and time, so consolidation never takes two occurrences for one, and each is tagged
`ynf.failure.v1`, which is how to select ynf's records.

```
sig/ci-diverges/golangci-lint/errcheck
sig/stuck/sensor:unit-tests/test:TestFoo
sig/budget/turns/harness:ynh-lint@1.4
```

ynm's dream pass already turns three or more episodic memories with the same subject into a
reflective one. With a good signature, "this keeps happening" falls out of ynm without ynf doing
anything clever. ynf tags each record `occurrence`, so ynm's dedupe and contradiction passes leave
the records alone and reflection still counts every one.

## Where the memory goes

- **Into the agent, through its own harness.** ynf does not put memory into an agent's task: the
  orchestrator would be deciding what an agent should remember, and the same item could get a
  different prompt on each attempt. A harness that wants memory connects its agent to ynm itself,
  and can read the same namespace ynf writes to.
- **Into people.** `ynf stats` lists each lane's top signatures with ynm's summaries. A signature
  that keeps recurring can open an issue on the harness's repository proposing a new sensor or a
  focus change. ynf proposes harness changes; it never makes them.

## Why memory never decides

ynm's recall is ranked and fuzzy, and its reflections are written by a model. If a decision read
from memory, the same event could produce different decisions depending on what the memory store
happened to hold, and replaying a decision would stop meaning anything.

So ynf keeps its own counters, per item and per signature, incremented from the same observations
it writes to ynm. Lane rules read the counters: "this signature has hit three times on this item,
escalate". Memory carries the explanation; counters carry the decision.

See [ADR-008](../adr/008-memory-with-ynm.md).
