# 3. Which model, at what cost

Once a factory runs unattended, the questions change from "did it work" to "is it worth it": which
lanes need a bigger model, which could use a smaller one or less effort, and what each merged
change cost. ynh reports what every run spent; ynf keeps that with the run and adds up what
happened to the work afterwards. This lesson reads both.

It needs the converged run from lesson 2. Merging its pull request, at the end, runs no model.

## Prerequisites

The block from [the track's README](README.md#every-lesson-starts-here), and the item from lesson 2:

```bash
export YNF_SRC=<your ynf checkout>
set -a; . "$YNF_SRC/sandbox/sandbox.env"; set +a
export REPO=$SANDBOX_OWNER/${SANDBOX_NAME:-ynf-sandbox}
export TUTORIAL=$HOME/ynf-factory-tutorial
export YNF_CONFIG=$TUTORIAL/config.yaml
export YNM_HOME=$TUTORIAL/ynm-home
cd "$TUTORIAL"
L=$(gh issue list -R $REPO --search 'Unchecked errors in internal/store in:title' --json number -q '.[0].number')
```

## The run record

Every run is recorded in the item's log in ynf's own store, with what the runner reported:

```bash
ynf --format json items log $REPO#$L | python3 -c '
import json, sys
for e in json.load(sys.stdin):
    if e["kind"] == "run":
        r = e["body"]
        print(json.dumps({k: r.get(k) for k in ["runner", "executor", "outcome", "model", "backend", "effort",
            "turns", "tokens", "input_tokens", "output_tokens", "cache_read_tokens", "cost_usd",
            "harness", "runner_version", "auto_approve", "denied"]}, indent=2))'
```

Expected: two records, lesson 1's refused run (`operator_error`, nothing reported) and then lesson
2's:

```json
{
  "runner": "ynh",
  "executor": "docker",
  "outcome": "converged",
  "model": null,
  "backend": "claude",
  "effort": "medium",
  "turns": 1,
  "tokens": 2060,
  "input_tokens": 23,
  "output_tokens": 2037,
  "cache_read_tokens": 119551,
  "cost_usd": 0.1444593,
  "harness": "local/ynf-sandbox@0.1.0",
  "runner_version": "0.9.0",
  "auto_approve": "edits",
  "denied": ["raw.githubusercontent.com"]
}
```

Three of these matter when comparing runs later:

- **The harness and its version.** A change to the harness's prompt or sensors changes results as
  much as a change of model. A comparison across a harness change is a comparison of two things.
- **The token split.** `tokens` is input plus output. A long session's real cost is mostly
  `cache_read_tokens`: here 119,551 tokens read from cache, against 2,060 written and read fresh.
- **Cost.** ynf records the cost ynh reports and never prices tokens itself. A runner that reports
  no cost leaves it empty, not zero.

This record lives in ynf's store, not in ynm. ynf's store is the history of every run, and lesson 4
shows what ynm gets instead.

## Stats by model and effort

```bash
ynf stats --lane lint-paydown
```

Expected:

```text
REPO               LANE          ITEMS  PROPOSED  MERGED  REJECTED  YIELD  STATUS
<you>/ynf-sandbox  lint-paydown  1      1         0       0         -      running

<you>/ynf-sandbox lint-paydown, top failure signatures:
     1  sig/egress/denied/raw.githubusercontent.com

<you>/ynf-sandbox lint-paydown, by model and effort:
  MODEL                        EFFORT  RUNS  CONVERGED  TURNS/RUN  TOKENS/RUN  CACHED/RUN  COST   PROPOSED  MERGED  REJECTED
  claude (model not reported)  medium  1     1          1.0        2060        119551      $0.14  1         0       0
```

(Your figures will differ, and the signature appears only if your run was refused a host. There is
also a row for lesson 1's refused run, with `0` converged: a run refused before it starts is
recorded without its runner, so that row's model reads ` (model not reported)`.)

The first table is the lane's yield: of the proposals people have decided on, the share they merged. Nothing is decided yet, so it's `-`. The last breaks
the lane's runs down by model and effort: how often each converged, what a run took and cost, and
what happened to the changes it proposed. A proposal, merge or rejection counts for the model of
the run whose change was proposed.

**`claude (model not reported)`** is honest rather than broken. The lane didn't name a model, so
Claude used its default, and ynh reports the backend but not which model that was. When ynh reports
the model a run actually used ([eyelock/ynh#441](https://github.com/eyelock/ynh/issues/441)), this
column names it for unpinned runs too, and nothing in ynf changes. Until then every run on the
default model is grouped here. A command lane has no model at all: its row says `none (command)`,
and its turns, tokens and cost are `-`, never reported rather than zero.

The effort column is read the same way: it is what ynh reports the run used. A lane can't set it,
because ynh has no way to be told an effort level yet.

## Pin a model, and compare

A lane can name the model its agent runs on: `run.ynh.model`, passed to ynh as `--model`. Leave it
out and the vendor chooses, as above. With two copies of a lane on different models, `ynf stats`
puts them side by side.

Make the copy in the configuration repository, where lesson 1 tried the budget. It has to be a
whole lane, since there is no lane of that name to lay keys over; give it an intake label nothing
carries, so only the `ynf start` below ever feeds it:

```bash
cd "$TUTORIAL/factory"
cat >> .agents/factory/lanes.yaml <<'EOF'
  lint-paydown-pinned:
    kind: originate
    intake:
      - github.search: 'repo:eyelock/ynf-sandbox is:issue is:open label:"ynf:lint-pinned"'
        every: 5m
    run:
      runner: ynh
      env: [ANTHROPIC_API_KEY]
      ynh:
        harness: .
        base: ynf-sandbox-agent:latest
        focus: tidy
        auto_approve: edits
        model: sonnet
        sensor_scope:
          lint: 'GOLANGCI_LINT_CACHE="$PWD/.cache/golangci-lint" golangci-lint run ./{label.pkg}/...'
          test: 'go test -count=1 ./{label.pkg}/...'
          docs: 'sh scripts/check-docs.sh {label.pkg}'
    when:
      converged: open_pr
      ci_failed: { retry: 2, then: escalate }
      outcome.budget: { retry: 1, then: escalate }
      outcome.stuck: escalate
EOF
git commit -qam "lint-paydown-pinned: the lint lane on sonnet" && git push -q
cd "$TUTORIAL"
```

`model` takes a plain name: letters, digits and `.`, `-`, `_`, `/`, `:`, in whatever form the vendor
accepts. For Claude that's an alias such as `sonnet` or a full name such as `claude-sonnet-5-5`. A
name with a space or a `$(` is refused when the lanes load, because it ends up on ynh's command
line. Use a model other than your default, or the two rows will be the same model twice.

Run it on the sandbox's other plain lint ticket, the `internal/report` one, and look again:

```bash
R=$(gh issue list -R $REPO --search 'ineffassign and staticcheck findings in internal/report in:title' --json number -q '.[0].number')
ynf start $REPO#$R --lane lint-paydown-pinned
ynf stats --lane lint-paydown-pinned
```

Expected: a draft pull request as in lesson 2, and a row that names the model instead of saying it
wasn't reported, `claude/sonnet` or, on a ynh that resolves aliases, the full name it ran, such as
`claude/claude-sonnet-5-5`. `ynf stats` without `--lane` lists both lanes. The run on the default
model still reads `claude (model not reported)`. The two lanes ran different tickets, so this is a
first look, not a controlled test: with a few dozen tickets through each lane the rows say whether
the pinned model converges as often, at what cost, and whether its changes merge as often. `ynf
harness` shows the pin beside the lane, as `model sonnet`.

## Merge it, and see the yield

Mark the draft ready and merge it, as a reviewer would. The sandbox's `main` requires its `lint`,
`test` and `docs` checks, so wait for them first:

```bash
PR=$(ynf --format json items show $REPO#$L | python3 -c 'import json,sys; print(json.load(sys.stdin)["pr"])')
gh pr checks $PR -R $REPO --watch
gh pr ready $PR -R $REPO
gh pr merge $PR -R $REPO --squash
```

ynf notices on its next look at the item. A proposed item's CI is checked every 30 seconds and an
item in review every 5 minutes, so wait a few minutes, then sweep the lane:

```bash
ynf sweep --lane lint-paydown
ynf stats --lane lint-paydown
```

Expected: the item is `done`, and the lane's row reads `1  1  1  0  1.00`: one item, proposed,
merged, none rejected, a yield of 1.00. The by-model row's MERGED is 1. If the item still says
`in_review`, its next look isn't due yet; sweep again in a minute.

With a few dozen items, these two tables answer the questions this lesson began with: a lane whose
runs converge but whose changes get rejected needs a better harness, not a bigger model; one whose
runs keep hitting their turn budget may need a bigger model or more effort; one that converges and
merges cheaply might do as well with less.

## What just happened

- **ynh** reported what the run used and spent: backend, effort, turns, the token split, cost, the
  harness and its own version.
- **ynf** recorded that with the run in its own store, then followed the change it proposed through
  review to the merge.
- **`ynf stats`** joined the two: what each model and effort spent, against what became of its work.
- **`run.ynh.model`** pinned a lane to a model, so two lanes could be compared. Effort is reported,
  not set.

Next: [4. When it keeps failing](04-when-it-keeps-failing.md).
