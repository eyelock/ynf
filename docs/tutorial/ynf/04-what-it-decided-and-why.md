# 4. What it decided and why

Everything ynf did in lesson 3 is on the record: every decision, run and action, in order. This
lesson reads that record, then replays it: first to show the decisions come out the same, then
under a lane you've changed, to see what ynf would have done differently.

You need the terminal from lesson 1, with `YNF_CONFIG` set, and `N` from lesson 3.

## The item's log

```bash
ynf items log <you>/ynf-sandbox#$N
```

Expected:

```text
2026-10-04T12:07:53Z  decision ynf.ticket.matched -> ready: eligible for lane gofmt
2026-10-04T12:07:55Z  action   label ok=true 
2026-10-04T12:07:55Z  decision ynf.timer.due -> running: ready: starting run (attempt 1) [run]
2026-10-04T12:07:57Z  run      01M43G7FJ1F1PMAHZS33Q6N945-1 command via docker: converged , 1 changed (1.2s)
2026-10-04T12:07:57Z  decision ynf.run.finished -> running: converged: proposing the change [open_pr]
2026-10-04T12:08:00Z  action   open_pr ok=true 
2026-10-04T12:08:01Z  decision ynf.action.done -> proposed: proposed as #13
2026-10-04T12:08:02Z  action   label ok=true 
```

Four kinds of entry, all appended, never changed:

- **`decision`:** the event, the state it led to, the reason, and in brackets what it asked to be
  done: `[run]`, `[open_pr]`.
- **`action`:** something ynf did on GitHub because a decision asked, and whether it worked.
- **`run`:** a run's outcome, with the runner and executor, the files it changed and how long it
  took.
- **`note`:** something a person did, such as a retry. You'll make one in lesson 6.

The log says what happened. For why, look at a decision in full:

```bash
ynf --format json items log <you>/ynf-sandbox#$N
```

Each decision there carries everything it was decided from. Under `input` are the lane, the item as
it was, the event, and the **facts**: the ticket, and once there is one the pull request, as ynf
read them from GitHub at that moment. Under `policy` are the commits the lanes were read at, from
the repository (`sha`) and the configuration repository (`config_sha`). Under `decision` are the
item it led to, the actions it asked for, and the reason.

## The item itself

```bash
ynf items show <you>/ynf-sandbox#$N
```

Expected, among the fields:

```json
  "key": "item/github.com/<you>/ynf-sandbox/issues/5",
  "kind": "originate",
  "lane": "gofmt",
  "ticket": {
    "host": "github.com",
    "key": "<you>/ynf-sandbox#5"
  },
  "forge": "github.com",
  "repo": "<you>/ynf-sandbox",
  "number": 5,
  "state": "proposed",
  "reason": "proposed as #13",
  "branch": "ynf/issue-5",
  "pr": 13,
```

The item is the current state; the log is how it got there. Its key, `item/github.com/…`, names
the ticket's host as well as its number, so the same `owner/name#5` on two GitHub servers is never
the same item.

## Replay

```bash
ynf replay <you>/ynf-sandbox#$N
```

Expected:

```text
same  ynf.ticket.matched recorded ready       replayed ready       eligible for lane gofmt
same  ynf.timer.due      recorded running     replayed running     ready: starting run (attempt 1)
same  ynf.run.finished   recorded running     replayed running     converged: proposing the change
same  ynf.action.done    recorded proposed    replayed proposed    proposed as #13
4 decisions, 0 differ
```

### What just happened

ynf took each recorded decision's inputs, the item, the event and the facts, and decided again
under the recorded lane. Nothing ran and nothing touched GitHub: deciding is a pure function, with
no clock, network or randomness of its own. The same inputs always give the same decision, so
every decision ynf has made can be checked after the fact.

## Replay under a different lane

Say you want a person to look at every formatting change before it becomes a pull request. Would
that have changed anything here? Edit `~/ynf-tutorial/gofmt.yaml`, from lesson 2, and change one
line:

```yaml
    when:
      converged: escalate      # was open_pr
      ci_failed: escalate
```

Then replay the same item under it:

```bash
ynf lanes validate --file ~/ynf-tutorial/gofmt.yaml
ynf replay <you>/ynf-sandbox#$N --policy ~/ynf-tutorial/gofmt.yaml
echo "exit $?"
```

Expected:

```text
/Users/you/ynf-tutorial/gofmt.yaml: valid, 1 lanes (gofmt)
same  ynf.ticket.matched recorded ready       replayed ready       eligible for lane gofmt
same  ynf.timer.due      recorded running     replayed running     ready: starting run (attempt 1)
DIFF  ynf.run.finished   recorded running     replayed escalated   converged
same  ynf.action.done    recorded proposed    replayed proposed    proposed as #13
4 decisions, 1 differ
ynf: 1 decisions differ
exit 32
```

The decision after the run differs: under your lane the item would have been escalated to a person
instead of proposed. Each decision is replayed from its own recorded inputs, so the one after it
still replays the same: it's checking that decision, not re-running the whole history. A
difference exits 32, so a script or a CI check can ask "does this lane change alter any recorded
decision?" before the change is merged.

Nothing changed in the sandbox. `--policy` only affects the replay; the lanes ynf uses are still
the ones on `main`.

## What you know now

- `ynf items log` is the item's whole history: decisions, actions, runs and notes. `--format json`
  shows what each decision was made from.
- `ynf items show` is the item as it is now.
- `ynf replay` decides again from the recorded inputs, and shows each decision is the same.
- `ynf replay --policy` asks what a different lane would have decided, without running anything,
  and exits 32 if any decision differs.

Next: [5. Running unattended](05-running-unattended.md).
