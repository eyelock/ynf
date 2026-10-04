# ADR-005: Claims, leases and fencing

Status: draft (2026-10-03)
Satisfies: FR-15, FR-16, FR-17, NFR-2, NFR-6

## Context

Many ynf instances may see the same item at once: a pool of workers sharing a store, a
developer's machine and a worker, or two CI workflow runs triggered by a push and a check completing a second apart. When
one of them picks an item, the others must not, and if that instance dies, the item must not be
stuck forever. A step can last an hour, because `ynh agent run` defaults to a 60 minute wall-clock
budget.

## Decision

**A claim is a compare-and-swap on the item document.** Due timers and events are hints; an
instance owns an item only once this write succeeds:

```json
"lease": {
  "owner": "ynf@host-a/4121",
  "epoch": 42,
  "step_id": "01J9Z3…",
  "acquired_at": "2026-10-03T09:14:05Z",
  "expires_at": "2026-10-03T09:15:35Z"
}
```

The write is `Put(item, doc, ifVersion)` and only succeeds when there is no lease or the lease
has expired. Every successful claim increments `epoch` and mints a new `step_id`.

**Heartbeat.** The holder renews by compare-and-swap on the same epoch every 30 seconds, with a 90
second TTL (default, 2026-10-03). If renewal fails, or has not succeeded for 70% of the TTL, the
holder assumes it has lost the item and stops: it sends `{"action":"interrupt"}` on
`ynh agent run`'s control channel, ynh checkpoints and exits 31, and the holder writes nothing
more.

**Fencing.** A holder can still act after losing its lease (a GC pause, a partition, a CI job
that was not cancelled in time). So:

- every store write in a step is conditional on `lease.epoch` still being the holder's, so a stale
  holder's commit fails
- every external side effect is idempotent, keyed by `step_id`: the branch name is deterministic
  (`ynf/<item>`), comments carry a hidden `<!-- ynf:step=<step_id> -->` marker, and the holder
  checks for its marker before acting

A duplicate or stale step therefore does nothing visible.

**Recovery.** While a lease is held, the item's timer is due just after the lease expires, or at
its `next_due` if that is sooner, and every heartbeat moves it forward with the lease. A live
holder's timer therefore never fires; a dead holder's fires about one TTL after the death, and any
instance's `Due()` sweep claims the item with `epoch + 1`. Releasing the lease puts the timer back
to `next_due`. Each claim of the same step increments
`attempts`; at the lane's cap (3 by default, 2026-10-03) the item moves to `quarantined` and is
escalated to a human instead of retried.

**Restart, not resume, on reclaim.** The new holder discards the dead holder's partial run and
re-runs the step from the last committed decision on a fresh worktree. The abandoned attempt
stays in the item's log, so it still counts in yield and cost.

**Clock skew.** S3 and DynamoDB offer no server timestamp to compare against, so the TTL is
generous relative to the renewal interval, and hosts are expected to run NTP. `ynf doctor`
reports skew against the store provider's `Date` header.

**In CI**, a GitHub Actions `concurrency: ynf-<item>` group cuts duplicate jobs early. It is an
optimisation; the lease is the authority, because sweeps and webhooks race across workflows.

## Alternatives

- **Resume the dead holder's run from ynh's checkpoint.** Keeps up to a full run's work, but the
  checkpoint and worktree live on the dead holder's disk. It needs the session directory synced to
  blobs and a pushed work-in-progress branch per turn. Left as a seam: the executor already
  emits `--emit-jsonl` to a per-step directory that a later version can sync.
- **A distributed lock service (etcd, Consul, DynamoDB lock client).** Another dependency per
  host, and still needs fencing for side effects.
- **Queue visibility timeouts as the lock.** Ties exclusivity to one transport and does nothing
  for the CI and developer hosts.

## Consequences

- A crash costs at most one step's budget, plus the lease TTL in latency.
- Every adapter that writes to a forge or ticket system must implement the idempotency check.
  This is part of each adapter's conformance test.

## Open questions

- Should the lane be able to say "resume on reclaim" once the checkpoint sync exists?

## History

- 2026-10-03: drafted.
