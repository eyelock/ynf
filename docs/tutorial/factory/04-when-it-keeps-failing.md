# 4. When it keeps failing

One failed run is a fact about one ticket. The same failure on ticket after ticket is a fact about
the factory: a sensor that's too strict, a harness prompt that sends the agent somewhere it can't
go, a test that's flaky. No single run can see that; ynf can, because it sees every run. This lesson
makes runs fail, follows each failure from ynf into ynm, and has ynm notice that one keeps
happening.

The first part runs a real agent that usually fails, which can cost more than a converging run: it
may use its whole turn budget, and the lane retries a run that hits it. The rest runs no model,
except the optional last step, which uses whatever model ynm writes with.

## Prerequisites

The block from [the track's README](README.md#every-lesson-starts-here), lessons 1 to 3 done, and
your model key:

```bash
export YNF_SRC=<your ynf checkout>
set -a; . "$YNF_SRC/sandbox/sandbox.env"; set +a
export REPO=$SANDBOX_OWNER/${SANDBOX_NAME:-ynf-sandbox}
export TUTORIAL=$HOME/ynf-factory-tutorial
export YNF_CONFIG=$TUTORIAL/config.yaml
export YNM_HOME=$TUTORIAL/ynm-home
cd "$TUTORIAL"
export ANTHROPIC_API_KEY=<your key>
export NS=factory/github.com/$REPO
```

`NS` is the namespace ynf writes your sandbox's memories to: `factory/{repo}` by default, where
`{repo}` is the host and repository, so the same `owner/name` on two forges never share one.

## A run that can't win

The sandbox's `clock.Stamp ignores write errors` ticket is a fair lint fix with a trap in it: the
package's tests include `TestSince`, which fails about nine runs in ten whatever the agent does. The
lane scopes the test sensor to that package, so the run rarely converges.

```bash
C=$(gh issue list -R $REPO --search 'clock.Stamp ignores write errors in:title' --json number -q '.[0].number')
ynf start $REPO#$C --lane lint-paydown
```

Expected, usually: the agent fixes the lint finding, the test sensor keeps failing, and ynh gives
up. The last line is one of:

```text
github.com/<you>/ynf-sandbox#<n>: escalated in lane lint-paydown (outcome.stuck)
github.com/<you>/ynf-sandbox#<n>: escalated in lane lint-paydown (outcome.budget after 1 retries)
```

`outcome.stuck` is ynh's exit 13: the same sensor failing the same way, turn after turn.
`outcome.budget` is the turn cap; this lane's `when` retries that once before escalating. Now and
then `TestSince` passes, the run converges, and you get a draft pull request with a correct fix
instead. That's the flake doing its job. If it happens, retry the item and start it again:
`ynf items retry $REPO#$C`.

ynf turns each failure into a **signature**, a short, deterministic key, and counts it on the item:

```bash
ynf items show $REPO#$C | grep -A3 counters
```

Expected: `"sig/stuck/sensor:test": 1`, the sensor that stayed red, and for a run that hit its turn
cap, `"sig/budget/turns/harness:<harness>@<version>"` counted once per capped run, so twice if the
retry capped too. A signature names what failed, so the same failure on another ticket gets the
same name. A failure that names nothing more specific, such as a command that exited 2, is counted
as `sig/outcome/<outcome>`, and a failure is counted under one or the other, never both. These
counters are ynf's own. Its decisions and stop conditions read them, never memory.

## In ynm

Every time a signature's count goes up, ynf also writes that occurrence to ynm, as an episodic
memory whose subject is the signature:

```bash
ynm recall --namespace $NS --tags ynf.failure.v1
```

Expected: a line per occurrence: its score, id, type and store, then its summary. The `clock`
failure is there, and, if lesson 2's run was refused a host, so is that:

```text
0.875  <id>  episodic   personal  sig/stuck/sensor:test on github.com/<you>/ynf-sandbox#<n> (lint-paydown), occurrence 1, run <run id>
0.871  <id>  episodic   personal  sig/egress/denied/raw.githubusercontent.com on github.com/<you>/ynf-sandbox#<n> (lint-paydown), occurrence 1, run <run id>
```

Look at one whole:

```bash
ynm recall --namespace $NS --tags ynf.failure.v1 --limit 1 --json | python3 -m json.tool | head -40
```

Expected: the record ynf wrote. Its `subject` is the signature. Its tags are `ynf`,
`ynf.failure.v1`, `lane:lint-paydown`, `failure` and `occurrence`. Its `dataSchema` is `ynf.failure.v1`, and its
`data` has the signature, the item, the lane, the count, the run, the step, the time and the model.
Its content says the same in a sentence:

```text
Failure `sig/stuck/sensor:test` occurred on github.com/<you>/ynf-sandbox#<n> in lane `lint-paydown` at <time>: occurrence 1 on this item, run `<run id>`, step `<step id>`, model ``.
```

Every occurrence's text names its own item, run, step and time, and the `occurrence` tag tells ynm
that each record is one event in a series. That matters to ynm: its dream merges memories it judges
to say the same thing, and two occurrences of one failure must never be taken for one. With a ynm
that knows the tag, its dedupe and contradiction passes leave them alone and reflection still counts
them. ynm selects records by tag and namespace, not by `dataSchema`, so the tag is how to
find ynf's.

Steps themselves aren't written to ynm. The full history of every run is in ynf's store, as lesson
3 showed. ynm gets the failures, because noticing repeats is what ynm is for.

## Three in a row, for free

A reflection needs three occurrences of one subject. Agent runs are a costly way to get them, so
make a command lane fail instead. Ask gofmt to format a package that doesn't exist, three times,
as ad hoc work:

```bash
for i in 1 2 3; do
  ynf start --prompt "Format internal/nowhere, attempt $i" --label pkg:internal/nowhere --repo $REPO --lane gofmt
done
```

Expected: three items, each ending

```text
adhoc/<id>: escalated in lane gofmt (outcome.error)
```

`gofmt -w ./internal/nowhere` exits 2, which a command runner reports as `error`. Each item counts
`sig/outcome/error` once, and each count is an occurrence in ynm:

```bash
ynm recall --namespace $NS --subject sig/outcome/error
```

Expected: three lines, one per item, each `occurrence 1` of its own item.

## A reflection

ynm's dream has a **reflect** pass: for a subject with three or more episodes, it drafts a summary
of what they add up to, checks the draft against them, and keeps it only if nothing in it is
unsupported. Drafting needs a model. ynm uses the `claude` CLI when it's installed, or an
OpenAI-compatible endpoint; with neither, the pass is skipped. ynm's
[dreaming tutorial](https://github.com/eyelock/ynm/blob/main/docs/tutorial/08-dreaming.md) and
[how-to](https://github.com/eyelock/ynm/blob/main/docs/how-to/configure-judge-and-writer.md)
cover choosing one.

Run only the reflect pass, over the sandbox's namespace. ynf tags every failure record `occurrence`,
so a full dream (ynm 0.3.0 or later) would leave them alone too, never merging one occurrence into
another; the lesson needs only the reflection:

```bash
ynm dream --namespace $NS --passes reflect
```

Expected, with a writer:

```text
reflect: 1/1 changed
```

One subject, `sig/outcome/error`, had enough episodes, and its reflection was written. Without a
writer it's `reflect: 0/1 changed`. With a writer, a draft the check doubts is held back too; run
`ynm dream --namespace $NS --passes reflect --json` and its notes say which.

```bash
ynm recall --namespace $NS --type reflective
```

Expected: one reflective memory about `sig/outcome/error`, linked to the three episodes, saying
what they have in common: runs in the gofmt lane ended in an error three times, on three items,
and when. It reports only what the episodes say. It's the standing lesson, where each episode is
one event.

## Advisory, never instructions

None of this went back into a run. Look at the task the `clock` run was given:

```bash
cat work/steps/item_github.com_${SANDBOX_OWNER}_ynf-sandbox_issues_$C/*/run/task.md
```

Expected: the `tidy` focus's prompt, then the ticket, and nothing else. ynf never puts memory into
an agent's task. If it did, a run's prompt would depend on what the store held that day, and the
same ticket could get a different task on each attempt. ynf's own decisions read only its own
counters. Memory is for people, and `ynf stats` lists each lane's top signatures for them.

An agent can still use memory, if its harness says so: the harness connects the agent to ynm
itself, with its own configuration. That's ynm's
[connect an agent](https://github.com/eyelock/ynm/blob/main/docs/tutorial/07-connect-an-agent.md)
tutorial, and nothing to do with ynf.

## What just happened

- **ynh** ran the `clock` agent until its sensor stayed red, and exited `stuck` or at its budget.
- **ynf** turned each failure into a signature, counted it on the item for its own decisions, and
  wrote each occurrence to **ynm**, in the sandbox's namespace, tagged `ynf.failure.v1`.
- **ynm**'s dream counted three episodes of one subject, drafted what they add up to, checked it,
  and kept it as a reflection.

The orchestrator records, the memory notices, and a person decides what to do about it.

Next: [5. Shared memory](05-shared-memory.md).
