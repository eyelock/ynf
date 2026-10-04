# 2. A contained agent run

Lesson 1 stopped before any model ran. This one lets the agent work: ynf starts ynh in a
container, the agent edits the code, ynh's sensors judge it, and ynf turns the result into a draft
pull request. Along the way you'll see everything ynf puts around the agent so it can work
without anyone approving each edit: a container, a network that only reaches named hosts, and a
model key passed by name.

This lesson runs a real agent on your API key: about $0.15 to $0.25.

## Prerequisites

The block from [the track's README](README.md#every-lesson-starts-here), lesson 1 done (the agent
image built and the lint item back to ready), and your model key:

```bash
export YNF_SRC=<your ynf checkout>
set -a; . "$YNF_SRC/sandbox/sandbox.env"; set +a
export REPO=$SANDBOX_OWNER/${SANDBOX_NAME:-ynf-sandbox}
export TUTORIAL=$HOME/ynf-factory-tutorial
export YNF_CONFIG=$TUTORIAL/config.yaml
export YNM_HOME=$TUTORIAL/ynm-home
cd "$TUTORIAL"
export ANTHROPIC_API_KEY=<your key>
L=$(gh issue list -R $REPO --search 'Unchecked errors in internal/store in:title' --json number -q '.[0].number')
```

## Run it

```bash
ynf start $REPO#$L --lane lint-paydown
```

Expected: about a minute or two of log, then a draft pull request. The lines that matter, trimmed:

```text
level=INFO msg="run started" item=item/github.com/<you>/ynf-sandbox/issues/<n> run=<run id> lane=lint-paydown runner=ynh executor=docker image=ynf-harness:<16 hex> base=main attempt=1
level=INFO msg="run in progress" item=... elapsed=30s turns=1 last=turn_start
level=INFO msg="run finished" item=... outcome=converged exit=0 duration=50.2s changed=1 denied=raw.githubusercontent.com model="" turns=1 tokens=2060 detail=""
level=INFO msg=action item=... action=open_pr ok=true pr=<pr> detail=""
github.com/<you>/ynf-sandbox#<n>: proposed in lane lint-paydown (proposed as #<pr>)
pull request: https://github.com/<you>/ynf-sandbox/pull/<pr>
```

A run is never silent: while the agent works, ynf logs `run in progress` every 30 seconds with the
turn it's on, read from ynh's trajectory. Your `denied` may be empty, or name a different host; the
next sections explain it.

Open the pull request. The change is to `internal/store` and nowhere else, its commit carries ynf's
trailers, and its description says which lane, runner and executor made it.

## What ynf put around the agent

Before ynh started, ynf assembled the run. In order:

1. **A fresh worktree** of your sandbox at the default branch's head, and a **run folder** beside it
   for everything the run reads and writes.
2. **The task.** ynf wrote `task.md` into the run folder: the focus's prompt from the harness, then
   the ticket, quoted as its reporter wrote it. Nothing else goes in: not memory, not ynf's
   opinions.
3. **The agent image**, `ynf-harness:<16 hex>`, built in lesson 1. The tag names the harness's
   contents, so an unchanged harness is never rebuilt.
4. **A container** with every capability dropped, `no-new-privileges`, the worktree at `/work` and
   the run folder at `/run/ynf`, running as the image's own user.
5. **A network with one way out**: an internal Docker network whose only route is ynf's egress
   proxy, which allows the lane's `egress.allow` hosts (the Go module proxy and checksum database)
   plus the vendor's API host, `api.anthropic.com` for Claude. The lane never lists that one; ynf
   adds it for the vendor.
6. **The model key by name.** The lane's `env: [ANTHROPIC_API_KEY]` passes the variable into the
   container by name, so its value never appears on a command line or in a log.
7. **The command.** The image's entrypoint is `ynh agent run` with the harness, so ynf passes only
   the flags: `--task @/run/ynf/task.md --format json --emit-jsonl /run/ynf/trajectory.jsonl`,
   `--auto-approve edits`, and `--sensor-overlay` with the lane's three scoped sensors.

## Why `auto_approve` is safe here

An agent left to itself asks before editing a file. Nobody is there to answer, so the lane says
`auto_approve: edits` and ynh passes the vendor's own flag for it: the worker edits without asking,
and still can't run commands of its own.

ynf passes that only to a **contained** run, one in the container above. Run the same lane with the
uncontained `process` executor and ynf drops it with a warning: `auto_approve applies only inside
containment; this run keeps its approval prompts`. On your own machine, at a terminal, `ynf start
--auto-approve edits` is the attended equivalent: you are the one saying yes.

## The run folder

Everything the run produced is kept:

```bash
ls work/steps/item_github.com_${SANDBOX_OWNER}_ynf-sandbox_issues_$L/*/run
```

Expected:

```text
checkpoint.json  egress.jsonl  stderr  stdout  task.md  trajectory.jsonl
```

`stdout` is what ynh handed back: one JSON object, on every path, success or not.

```bash
python3 -m json.tool work/steps/item_github.com_${SANDBOX_OWNER}_ynf-sandbox_issues_$L/*/run/stdout | head -40
```

Expected, from a real run:

```json
{
    "capabilities": "0.9.0",
    "exit_code": 0,
    "converged": true,
    "backend": "claude",
    "auto_approve": "edits",
    "effort": "medium",
    "harness": { "name": "local/ynf-sandbox", "version": "0.1.0" },
    "budgets": { "max_turns": 12, "max_tokens": 1500000, "max_wall_ms": 1800000 },
    "budget_sources": { "turns": "manifest", "tokens": "manifest", "wall": "manifest" },
    "consumed": {
        "turns": 1,
        "tokens": 2060,
        "input_tokens": 23,
        "output_tokens": 2037,
        "cache_read_tokens": 119551,
        "cost_usd": 0.1444593,
        "wall_ms": 50211
    },
    "sensors": [
        { "name": "docs", "status": "pass", "stdout": "nothing documented for internal/store\n" },
        { "name": "lint", "status": "pass", "stdout": "0 issues.\n" },
        { "name": "test", "status": "pass", "stdout": "ok  \tgithub.com/<you>/ynf-sandbox/internal/store\t0.001s\n" }
    ]
}
```

(The sensors are trimmed; each also reports its category, tolerance, exit code, duration and tool
version.)

This is the contract between the two tools. ynh's exit code says how the run ended: 0 converged,
and others for a turn or token budget, stuck, tampering, or an operator error. ynf maps each to an
outcome and the lane's `when` decides what follows. `converged` opened a pull request; lesson 4
shows a run that isn't. The budgets came from the harness's manifest, since the lane didn't tighten
them. The sensors are the scoped ones: `internal/store`, not the whole repository.

A ynh **turn** is one round of the agent working and the sensors judging it, not one model call.
This run's agent made its fix in one turn, and all three sensors passed.

## The host it couldn't reach

`egress.jsonl` is the proxy's log, one line per connection:

```bash
cat work/steps/item_github.com_${SANDBOX_OWNER}_ynf-sandbox_issues_$L/*/run/egress.jsonl
```

Expected: mostly `api.anthropic.com`, allowed, and perhaps one that wasn't:

```json
{"time":"...","method":"CONNECT","host":"api.anthropic.com","port":"443","allowed":true}
{"time":"...","method":"CONNECT","host":"raw.githubusercontent.com","port":"443","allowed":false}
```

The agent tried to fetch something from GitHub's raw file host. The lane doesn't allow it, so the
proxy refused. The agent carried on without it and converged anyway. ynf still counts it, as the
failure signature `sig/egress/denied/raw.githubusercontent.com`:

```bash
ynf items show $REPO#$L | grep -A3 counters
```

Expected: `"sig/egress/denied/raw.githubusercontent.com": 1`. If it keeps happening, either the
lane needs that host or the harness's prompt should stop the agent reaching for it. Lesson 4 is
about noticing that.

## What just happened

- **ynf** decided the item was ready, made the worktree, wrote the task, and started the container
  with the egress proxy beside it, the key by name, and `--auto-approve edits` because the run was
  contained.
- **ynh** ran the agent with the harness's `tidy` focus, ran the three scoped sensors between turns,
  stopped when they all passed, and printed its result.
- **ynf** read the result and the proxy's log, recorded the run, saw the converged outcome, gated
  the diff against `pr.allowed_paths`, committed with its trailers, pushed, and opened the draft.

The agent never pushed, never saw a token for GitHub, and never reached a host the lane didn't name.

Next: [3. Which model, at what cost](03-which-model-at-what-cost.md).
