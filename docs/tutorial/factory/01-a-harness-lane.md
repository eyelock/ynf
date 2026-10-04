# 1. A harness lane

In track 1 every lane ran a command: `gofmt -w ./internal/format`, done in milliseconds. A lane can
instead hand the work to an agent: ynh, running a **harness**. This lesson puts the two kinds of
lane side by side, reads the harness ynf will hold the agent lane to, and breaks the rule that
binds them, on purpose, to see ynf refuse before any model runs.

## Prerequisites

The block from [the track's README](README.md#every-lesson-starts-here), then a folder for this
track, its config, and a ynm store of its own:

```bash
export YNF_SRC=<your ynf checkout>
set -a; . "$YNF_SRC/sandbox/sandbox.env"; set +a
export REPO=$SANDBOX_OWNER/${SANDBOX_NAME:-ynf-sandbox}
export TUTORIAL=$HOME/ynf-factory-tutorial
export YNF_CONFIG=$TUTORIAL/config.yaml
export YNM_HOME=$TUTORIAL/ynm-home
mkdir -p "$TUTORIAL" && cd "$TUTORIAL"
cat > config.yaml <<EOF
version: 1
factory: { repo: $SANDBOX_OWNER/${SANDBOX_FACTORY:-ynf-sandbox-factory} }
memory: { provider: ynm }
EOF
ynm init --personal
```

Expected: `personal store created at <your home>/ynf-factory-tutorial/ynm-home/store.git`. Memory
waits until lesson 4, but turning it on now means every run from here is remembered.

Start from a fresh sandbox, so its issues and lanes are as the seed made them:

```bash
make -C "$YNF_SRC/sandbox" reset
```

## Two kinds of lane

Ask ynf how the gofmt lane runs, then the lint lane:

```bash
ynf lanes show --repo $REPO gofmt
ynf lanes show --repo $REPO lint-paydown
```

Expected: each lane as JSON, with the defaults applied. The part that differs is `run`. The gofmt
lane runs a command in a Go image:

```json
"run": {
  "runner": "command",
  "executor": "docker",
  "image": "golang:1.26-alpine",
  "egress": { "allow": [] },
  "command": { "argv": ["gofmt", "-w", "./{label.pkg}"] }
}
```

The lint lane runs ynh, and names a harness rather than a command:

```json
"run": {
  "runner": "ynh",
  "executor": "docker",
  "egress": { "allow": ["proxy.golang.org", "sum.golang.org", "storage.googleapis.com"] },
  "env": ["ANTHROPIC_API_KEY"],
  "ynh": {
    "harness": ".",
    "base": "ynf-sandbox-agent:latest",
    "focus": "tidy",
    "auto_approve": "edits",
    "sensor_scope": {
      "docs": "sh scripts/check-docs.sh {label.pkg}",
      "lint": "GOLANGCI_LINT_CACHE=\"$PWD/.cache/golangci-lint\" golangci-lint run ./{label.pkg}/...",
      "test": "go test -count=1 ./{label.pkg}/..."
    }
  }
}
```

`harness: .` means the harness the repository carries, in `.agents/harness/`. The lane decides
**whether, when and which**: which issues, which harness, which of its focuses, and how tightly. The
harness decides **how**: the prompt for each focus, the turn and token budgets, and the sensors that
judge the work. Lesson 2 covers `egress`, `env` and `auto_approve`.

## The harness

Clone your sandbox and read its harness:

```bash
gh repo clone $REPO "$TUTORIAL/ynf-sandbox" -- -q
cat "$TUTORIAL/ynf-sandbox/.agents/harness/plugin.json"
```

Expected: ynh's manifest for a harness called `ynf-sandbox`. Three parts matter to ynf:

```json
"focuses": {
  "tidy":   { "prompt": "Fix the lint findings described in the task, in the package the task names and nowhere else. ..." },
  "docs":   { "prompt": "Make the documentation match the code, ..." },
  "fix-ci": { "prompt": "A pull request's CI is failing; ..." }
},
"agent": { "max_turns": 12, "max_tokens": 1500000, "max_wall": "30m" },
"sensors": {
  "lint": { "tolerance": "blocking", "source": { "command": "golangci-lint run ./..." } },
  "test": { "tolerance": "blocking", "source": { "command": "go test -count=1 ./..." } },
  "docs": { "tolerance": "blocking", "source": { "command": "sh scripts/check-docs.sh" } }
}
```

The **sensors** are what ynh runs between the agent's turns to decide whether the work is done: a
run converges when every blocking sensor passes. Across the whole repository, `golangci-lint run
./...` would fail on every other issue's debt, so the lane **scopes** each sensor to the package the
ticket is about. `{label.pkg}` comes from the issue's `pkg:` label, as it did for gofmt in track 1.

ynf asks ynh to describe what it can. On your laptop the harness is still inside the repository, so
there's nothing installed to read yet:

```bash
ynf harness $REPO
```

Expected:

```text
<you>/ynf-sandbox
  ok    deps (off): originate lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    doc-drift: originate lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    fix-ci: adopt lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    gofmt: originate lane, command on docker
        a command, not a harness
  ok    lint-paydown: originate lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    reclaim: originate lane, command on docker
        a command, not a harness
```

## The agent image

A harness lane runs in an image ynf builds from the harness, with ynh: `ynh image`, on the base the
lane names. The sandbox's base is ynh's own release image plus the Go toolchain and golangci-lint,
the tools its sensors run. Build it once:

```bash
make -C "$YNF_SRC/sandbox" agent-image
```

Expected: the image's ynh answering its version as the last line, `0.9.0` or later. The image is
`ynf-sandbox-agent:latest`, which is the lint lane's `base`.

## Loosen a budget, and be refused

A lane may tighten the harness's budgets. It may never loosen them: the harness's author set a
ceiling for what a run of it may spend. Try it as the factory's operator would: in the
configuration repository, whose `lanes.yaml` lies under every enrolled repository's lanes, key by
key. Give the lint lane 20 turns, more than the harness's 12:

```bash
gh repo clone $SANDBOX_OWNER/${SANDBOX_FACTORY:-ynf-sandbox-factory} "$TUTORIAL/factory" -- -q
cd "$TUTORIAL/factory"
cat >> .agents/factory/lanes.yaml <<'EOF'
  lint-paydown:
    run:
      ynh:
        budgets: { max_turns: 20 }
EOF
git commit -qam "lint-paydown: 20 turns" && git push -q
cd "$TUTORIAL"
ynf lanes show --repo $REPO lint-paydown | grep budgets
```

Expected: two lines, the merged lane's `"budgets"` and, in `sources`,
`"run.ynh.budgets.max_turns": "config@<sha>"`. The sandbox's own lane doesn't set budgets, so the
configuration repository's value comes through the merge. (The sandbox's `main` is protected, with
required checks, so the configuration repository is also the quicker place to experiment.)

Start the lint ticket for `internal/store`:

```bash
L=$(gh issue list -R $REPO --search 'Unchecked errors in internal/store in:title' --json number -q '.[0].number')
ynf start $REPO#$L --lane lint-paydown
```

Expected: ynf builds the harness image the first time, which takes a minute, and then stops. The
last line is:

```text
github.com/<you>/ynf-sandbox#<n>: escalated in lane lint-paydown (outcome.operator_error)
```

The log says why:

```bash
ynf items log $REPO#$L
```

Expected: a `run` line ending in

```text
operator_error the lane does not fit its harness: max_turns 20 loosens the harness's 12, 0 changed (...)
```

ynh never started an agent, so this cost nothing. The same check refuses a `sensor_scope` that
names a sensor the harness doesn't declare: `sensor_scope names "security", which the harness does
not declare`. A lane scopes the harness's sensors; it can't invent new ones.

Put the lane back, and the item with it:

```bash
cd "$TUTORIAL/factory" && git revert --no-edit -q HEAD && git push -q && cd "$TUTORIAL"
ynf items retry $REPO#$L
```

Expected: `item/github.com/<you>/ynf-sandbox/issues/<n>: back to ready`. The item waits for
lesson 2.

## What just happened

- **ynf** read the lane from your sandbox and the configuration repository, each at its current
  commit, and merged them: a ynh runner, harness `.`, focus `tidy` and three sensor scopes from the
  sandbox, and a turn budget from the configuration repository.
- **ynh** built the harness into an agent image when ynf asked (`ynh image ... --entrypoint agent
  --base ynf-sandbox-agent:latest`), tagged by the harness folder's contents, so it's built again
  only when the harness or its base changes.
- **ynf** asked the image's own ynh what harness it carries, and held the lane to it: 20 turns
  loosens 12, so the run was an operator error before any model ran.

The lane and the harness are owned separately on purpose: a harness author sets the ceiling, and a
factory operator can only work beneath it.

Next: [2. A contained agent run](02-a-contained-agent-run.md), where the agent actually runs.
