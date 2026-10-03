# One claim at a time

Several ynf instances can see the same work item at the same moment. This page explains how
exactly one of them acts on it, and what happens when that one dies halfway.

## Why it happens

A push and a check completing a second apart start two CI workflow runs. A hosted service has
several workers. A webhook and the reconciliation sweep both notice the same ticket. Without a
rule, two instances run the agent on one ticket, push to one branch, and post the same comment
twice.

## The claim

The work item is a document in the store, and every write to it is a compare-and-swap: "replace
this, but only if it is still the version I read". SQLite does that with a version column,
DynamoDB with a condition expression, and S3 with `If-Match` on the object's ETag.

To claim an item, an instance writes a lease into it: its name, an expiry ninety seconds out, and
an **epoch** one higher than the last. Only one compare-and-swap can win. The losers see a
conflict and move on.

## Holding it

A step can last an hour, because an agent run can. So the holder renews its lease every thirty
seconds. If it cannot renew, it assumes another instance has taken over and stops: it tells the
running agent to interrupt, and writes nothing more.

## When the holder is wrong about holding it

The hard case is a holder that does not know it has lost its lease: a long garbage collection
pause, a network partition, a CI job that was not cancelled in time. It wakes up and carries on.

Two things stop it doing harm. Every store write is conditional on the epoch still being its own,
so its commit fails. And every outside action is keyed by the step's id: the branch name is
fixed, every comment carries a hidden marker, and before acting the holder checks whether the
action is already done. A stale holder's actions find the work already done and do nothing.

This is called fencing. The lease decides who should act; the fence makes sure nobody else's
actions count.

## When the holder dies

Its lease expires. The item still has a due time, so the next sweep by any instance finds it,
claims it with the next epoch, and starts the step again from the last decision that was
committed. The half-finished run is thrown away rather than resumed: its checkpoint and worktree
were on the dead machine. The abandoned attempt stays in the item's history so it still counts
when measuring cost and yield.

Each reclaim counts as an attempt. After three, the item is quarantined and a human is asked,
rather than retried forever.

See [ADR-005](../adr/005-claims-leases-fencing.md).
