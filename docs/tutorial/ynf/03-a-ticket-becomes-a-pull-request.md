# 3. A ticket becomes a pull request

Take on one ticket by hand, watch ynf work it from start to finish, and look at what it leaves
behind: a branch, a draft pull request, a comment and labels on the issue.

You need the terminal from lesson 1, with `YNF_CONFIG` set.

## Pick a ticket

One of the sandbox's issues has no lane label, so no search will ever find it. It has to be started
by hand:

```bash
N=$(gh issue list -R <you>/ynf-sandbox --search 'started by hand in:title' --json number -q '.[0].number')
ynf ticket <you>/ynf-sandbox#$N
```

Expected:

```text
github.com/<you>/ynf-sandbox#5  internal/format is not gofmt-clean, started by hand
state   open
labels  pkg:internal/format

`internal/format` is not `gofmt`-clean.

This ticket has no lane label, so no search finds it: the acceptance test starts it with
`ynf start`, as a person or an automation would.
```

`ynf ticket` reads a ticket exactly as ynf would before taking it on, and changes nothing. Its
`pkg:internal/format` label is what the `gofmt` lane's `{label.pkg}` needs.

## Start it

```bash
ynf start <you>/ynf-sandbox#$N --lane gofmt
```

The first time, Docker pulls `golang:1.26-alpine`, which takes a minute. Then ynf logs each step as
it happens, on standard error:

```text
level=INFO msg=tracking item=item/github.com/<you>/ynf-sandbox/issues/5 lane=gofmt by=start
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/5 event=ynf.ticket.matched state=ready reason="eligible for lane gofmt"
level=INFO msg=action item=item/github.com/<you>/ynf-sandbox/issues/5 action=label ok=true add=ynf:working remove=ynf:fmt
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/5 event=ynf.timer.due state=running reason="ready: starting run (attempt 1)"
level=INFO msg="run started" item=item/github.com/<you>/ynf-sandbox/issues/5 run=01M43G7FJ1F1PMAHZS33Q6N945-1 lane=gofmt runner=command executor=docker image=golang:1.26-alpine base=main attempt=1
level=INFO msg="run finished" item=item/github.com/<you>/ynf-sandbox/issues/5 run=01M43G7FJ1F1PMAHZS33Q6N945-1 outcome=converged exit=0 duration=1.2s changed=1 denied="" model="" turns=0 tokens=0 detail=""
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/5 event=ynf.run.finished state=running reason="converged: proposing the change"
level=INFO msg=action item=item/github.com/<you>/ynf-sandbox/issues/5 action=open_pr ok=true pr=13 detail=""
level=INFO msg=decided item=item/github.com/<you>/ynf-sandbox/issues/5 event=ynf.action.done state=proposed reason="proposed as #13"
level=INFO msg=action item=item/github.com/<you>/ynf-sandbox/issues/5 action=label ok=true add=ynf:proposed remove=ynf:working
```

(Each line also starts with its time, left out here.) When it stops, it prints where the item got
to, on standard output:

```text
github.com/<you>/ynf-sandbox#5: proposed in lane gofmt (proposed as #13)
pull request: https://github.com/<you>/ynf-sandbox/pull/13
```

### What just happened

ynf turned the issue into an **item**, its record of one piece of work, and moved it through
states, one decision at a time:

| Event | State | Why |
|---|---|---|
| `ynf.ticket.matched` | `ready` | The lane takes this ticket. |
| `ynf.timer.due` | `running` | Ready work is due at once, so a run starts: attempt 1. |
| `ynf.run.finished` | `running` | The run converged with one file changed, so the change is proposed. |
| `ynf.action.done` | `proposed` | The pull request is open; now it waits on CI. |

Every decision has an **event** that caused it, the **state** it moved to, and a **reason**. The
decisions are made by a pure function of the item, the event and facts read fresh from GitHub. That
matters in lesson 4.

The run itself happened in a container on a fresh worktree of `main`. `gofmt` changed one file, and
ynf, not the run, committed it and pushed the branch: a run never pushes anything itself.

`start` stopped at `proposed` because the next thing to happen is outside ynf: CI has to run. It
doesn't sit and wait. Lesson 5 shows how ynf comes back to it.

## The pull request

```bash
gh pr view 13 -R <you>/ynf-sandbox --json title,isDraft,headRefName,body \
  -q '"\(.title)\ndraft: \(.isDraft), branch: \(.headRefName)\n\n\(.body)"'
```

Expected:

```text
ynf(gofmt): internal/format is not gofmt-clean, started by hand
draft: true, branch: ynf/issue-5

Closes #5.

Proposed by **ynf**: lane `gofmt`, runner `command`, executor `docker`.

| | |
|---|---|
| Item | `item/github.com/<you>/ynf-sandbox/issues/5` |
| Run | `01M43G7FJ1F1PMAHZS33Q6N945-1` (converged, 1.2s) |
| Changed | 1 file(s) |

A human reviews and merges this; ynf never merges.

<!-- ynf:item=item/github.com/<you>/ynf-sandbox/issues/5 -->
```

It's a **draft**, on the branch `ynf/issue-5`. Its title names the lane, and its body links the
issue, the item and the run. The last line is hidden on GitHub and names the item again. ynf finds
the pull request again by its branch: a second attempt at the same item pushes to `ynf/issue-5`
and reuses the pull request rather than opening another. Now the commit:

```bash
gh pr view 13 -R <you>/ynf-sandbox --json commits -q '.commits[0].messageBody'
```

Expected:

```text
Proposed by ynf for github.com/<you>/ynf-sandbox#5, lane gofmt.

YNF-Item: github.com/<you>/ynf-sandbox#5
YNF-Step: 01M43G7FJ1F1PMAHZS33Q6N945
YNF-Run: 01M43G7FJ1F1PMAHZS33Q6N945-1
```

Those are **trailers**: ynf's attribution, in the commit message, where they survive a squash
merge. From any commit on `main`, they lead back to the item, the step that made the decision and
the run that made the change. The run's id is the step's, with the attempt number after it.

## The issue

```bash
gh issue view $N -R <you>/ynf-sandbox --comments
```

Expected: the labels `pkg:internal/format` and `ynf:proposed`, and one comment from you (ynf works
with your token):

```text
**ynf** proposed #13.

<!-- ynf:open_pr=01M43G7FJ1F1PMAHZS33Q6N945 -->
```

The hidden marker names the step that wrote the comment, so ynf never posts the same comment twice.
The lane's `labels` moved the issue along: `ynf:working` while the run was going, then
`ynf:proposed`. Labels are for people; ynf never reads them back to decide anything.

## The item

```bash
ynf items ls
```

Expected:

```text
ITEM                              LANE   STATE     PR   REASON
github.com/<you>/ynf-sandbox#5    gofmt  proposed  #13  proposed as #13
```

Leave the pull request as it is: don't merge it until the end of the track. Later lessons start
other tickets for the same package, and each needs `main` as it is now.

## What you know now

- `ynf start` takes on one ticket now. It checks the ticket, the repository and the lane first,
  and refuses before creating anything if one doesn't fit.
- ynf keeps an **item** per piece of work and moves it through **states** by **decisions**, each
  with an event and a reason.
- A run changes files; **ynf** commits, pushes, opens the draft pull request and labels the issue.
  Trailers on the commit lead back to the item and the run.
- ynf never merges. A person does.

Next: [4. What it decided and why](04-what-it-decided-and-why.md).
