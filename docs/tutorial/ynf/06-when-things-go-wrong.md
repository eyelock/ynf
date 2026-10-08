# 6. When things go wrong

Machines die mid-run, people need to step in, and some lanes should stop for a while. This lesson
kills ynf in the middle of a run and watches another ynf pick the work up. Then it covers the
controls a person has: retry, release, pause and resume, and the numbers that say how a lane is
doing.

You need the terminal from lesson 1, with `YNF_CONFIG` set, and a second terminal with the same
exports.

## A slow lane

The `reclaim` lane does what `gofmt` does, but its command waits twenty seconds first:

```yaml
      command:
        argv: ['sh', '-c', 'sleep 20 && gofmt -w ./{label.pkg}']
```

That leaves time to kill ynf while the run is in progress. You paused it in lesson 1; resume it:

```bash
ynf resume reclaim --reason "lesson 6: the crash test"
```

Expected:

```text
<you>/ynf-sandbox/reclaim: resumed (lesson 6: the crash test)
```

## Kill ynf mid-run

In the first terminal:

```bash
ynf sweep --until-settled --timeout 15m --lane reclaim
```

Wait for the run to start:

```text
level=INFO msg="run started" item=item/github.com/<you>/ynf-sandbox/issues/10 run=01M43J5T0B9N8Q2XW4R7E1KZ3C-1 lane=reclaim runner=command executor=docker image=golang:1.26-alpine base=main attempt=1
```

If the item was already tracked in lesson 5, waiting in its paused lane, this can take up to a
minute: a waiting item checks again every `poll.review`. As soon as the line appears, kill ynf from
the second terminal, the way a machine dies, with no chance to clean up:

```bash
pkill -9 -f 'ynf sweep'
```

The first terminal says the process was killed. Now look at what it left behind:

```bash
R=$(gh issue list -R <you>/ynf-sandbox --search 'gofmt-clean, slowly in:title' --json number -q '.[0].number')
ynf items ls
ynf items show <you>/ynf-sandbox#$R
```

Expected, the item still `running`:

```text
github.com/<you>/ynf-sandbox#10   reclaim       running    ready: starting run (attempt 1)
```

and among its fields, the claim the dead process still holds:

```json
  "lease": {
    "owner": "ynf@your-laptop.local/48213",
    "epoch": 1,
    "step_id": "01M43J5T0B9N8Q2XW4R7E1KZ3C",
    "acquired_at": "2026-10-04T15:12:40.512Z",
    "expires_at": "2026-10-04T15:13:10.512Z"
  },
```

### What just happened

Before working an item, a ynf **claims** it with a **lease**, for `lease.ttl`: thirty seconds in
your config. While it works, it renews the lease every `lease.heartbeat`. Only the holder may change
the item, and every change it makes is checked against the lease's `epoch`. So a ynf that has lost
its claim, because it stalled and another took over, can't overwrite the newer holder's work.

The killed process will never renew its lease, so it runs out thirty seconds after it was taken.
Until then, the item is still someone else's.

## Another ynf takes over

Try to put the item back by hand while it is still `running`:

```bash
ynf items retry <you>/ynf-sandbox#$R
```

Expected, with exit code 2:

```text
ynf: item/github.com/<you>/ynf-sandbox/issues/10 is running; retry is only for escalated or quarantined items
```

ynf won't take a running item out from under the process that holds it. You don't need to do
anything: start a fresh ynf, as a restarted machine would:

```bash
ynf sweep --until-settled --timeout 15m --lane reclaim
```

About thirty seconds after the kill, it decides:

```text
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/10 event=ynf.timer.due state=running reason="the last attempt did not finish: starting run (attempt 2)"
level=INFO msg="run started" item=item/github.com/<you>/ynf-sandbox/issues/10 run=01M43J6Z3V5H0R9WK2T8C4N7DQ-1 lane=reclaim runner=command executor=docker image=golang:1.26-alpine base=main attempt=2
```

Twenty seconds later the run converges, a pull request opens, and when CI is green the sweep
settles with the item in review.

### What just happened

The item's timer was set to just after the lease ran out. When it fired, the claim was free, and a
run had started without ever reporting back, so the new ynf **restarted** the work as attempt 2: a
new run, on a fresh worktree of `main`. It didn't try to **resume** what the dead run was doing.
Nothing about a half-finished run can be trusted: its files might be half-written, its container
might still be running. Starting again from a known state is always safe, because a run only
changes a worktree, and only ynf commits and pushes from it.

A lane allows three attempts by default (`attempts`). An item whose runs keep dying is
**quarantined**: `3 attempts without a finished run`. It then waits for a person.

## Retry and release

Two commands put a person in charge of one item:

- **`ynf items retry <item>`** sends an escalated or quarantined item back to `ready`, with its
  attempts and failure counts cleared, to be run again on the next sweep. It's refused for an item
  in any other state, as you saw, and while another ynf holds the item.
- **`ynf items release <item>`** clears a lease left by a ynf that died, and makes the item due
  now, so another ynf restarts its work at once instead of waiting for the lease to run out. With a
  thirty-second lease you'd rarely bother; with a deployed factory's longer one, you might.

Both are recorded in the item's log as a `note`:

```text
2026-10-04T15:20:03Z  note     {"action":"retry","by":"human"}
```

## Pause and resume

Pausing works on a whole lane. You've used it since lesson 1:

```bash
ynf pause gofmt --reason "freeze while main is being released"
ynf items ls
```

Items already in flight carry on: a pull request still gets its CI checked and moves to review.
Nothing new starts. A ticket found while the lane is paused is tracked and waits in `ready`, with
the reason `lane gofmt is paused (freeze while main is being released); waiting`. Resuming takes a
reason too:

```bash
ynf resume gofmt --reason "release done"
```

A reason is required both ways. Changing what a factory may do is a deliberate act, recorded with
who did it and why.

## How the lanes are doing

```bash
ynf stats --lane gofmt --lane reclaim --lane lint-paydown
```

Expected:

```text
REPO                 LANE          ITEMS  PROPOSED  MERGED  REJECTED  YIELD  STATUS
<you>/ynf-sandbox    gofmt         3      3         0       0         -      running
<you>/ynf-sandbox    lint-paydown  5      0         0       0         -      paused: the ynf track runs no agents
<you>/ynf-sandbox    reclaim       1      1         0       0         -      running

<you>/ynf-sandbox gofmt, by model and effort:
  MODEL           EFFORT  RUNS  CONVERGED  TURNS/RUN  TOKENS/RUN  CACHED/RUN  COST  PROPOSED  MERGED  REJECTED
  none (command)  -       3     3          -          -           -           -     3         0       0

<you>/ynf-sandbox reclaim, by model and effort:
  MODEL           EFFORT  RUNS  CONVERGED  TURNS/RUN  TOKENS/RUN  CACHED/RUN  COST  PROPOSED  MERGED  REJECTED
  none (command)  -       1     1          -          -           -           -     1         0       0
```

A lane's **yield** is the share of its decided proposals that people merged: merged, divided by
merged and rejected. It shows `-` until a pull request has been merged or closed. The `reclaim` row
shows one run, not two: the killed run never reported an outcome, so there's nothing to count but
the attempt.

The per-model tables come into their own with agents, in the factory track. A command runs no
model, so it says `none (command)` and reports no turns, tokens or cost.

A lane's `stop` settings act on these numbers by themselves. `max_open_proposals` holds new work
back while that many pull requests wait for review. `yield_floor` pauses the lane when its yield
falls below the floor, once `min_sample` proposals have been decided (twenty by default). That
pause is recorded with ynf as the one who paused it and the yield as the reason, and `stats` shows
it, just like yours.

## What you know now

- A ynf **claims** an item with a **lease**, renewed by a heartbeat and checked by its epoch.
- When a ynf dies mid-run, another **restarts** the work about one lease later, as a new attempt
  from a fresh worktree. It never resumes. Too many unfinished attempts quarantine the item.
- **`retry`** and **`release`** let a person send one item back; **`pause`** and **`resume`** stop
  and start a whole lane. All of them need a person's reason or leave a note.
- **`stats`** says how each lane is doing: proposals, merges, rejections, yield, and whether it's
  paused and why.

Next: [7. Tickets from elsewhere](07-tickets-from-elsewhere.md).
