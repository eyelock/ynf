# 1. A sandbox and what ynf can see

Build a sandbox of your own on GitHub, build ynf, write its config, and ask it what it can see.
Nothing runs yet: this lesson is about the pieces ynf works with, and how it tells you whether each
one is there.

## Build ynf

ynf's repository holds the sandbox too, so start from a clone, and build ynf from it. Installing
with Homebrew (`brew install eyelock/tap/ynf`) gives you the binary but not the sandbox, so build
from the clone, which needs only Go.

```bash
gh repo clone eyelock/ynf
cd ynf
make deps
make install
export PATH="$HOME/.ynf/bin:$PATH"     # add this to your shell profile
ynf version
```

Expected:

```text
ynf dev-develop-<sha>
```

`make install` puts three files in `~/.ynf/bin`: `ynf` itself, and two static Linux builds of it,
`ynf-linux-amd64` and `ynf-linux-arm64`. When a run happens in a container, ynf starts one of those
inside it as the container's egress proxy, the only way out to the network. ynf finds the tools it
works with, ynh, ynm and ynr, only on your `PATH`, as it finds any other command.

## Build the sandbox

The sandbox is two private repositories on your GitHub account, created by Terraform from
`sandbox/` in the clone:

- **`<you>/ynf-sandbox`:** a small Go project with problems in it, and an issue for each problem.
  This is the repository ynf works on.
- **`<you>/ynf-sandbox-factory`:** the factory's configuration repository. It says which
  repositories ynf works on, and gives them default lanes. You'll look inside it in lesson 2.

Tell the sandbox who you are:

```bash
cp sandbox/sandbox.env.example sandbox/sandbox.env
```

Edit `sandbox/sandbox.env` and set `SANDBOX_OWNER` to your GitHub user or organisation. The other
settings have defaults: `SANDBOX_NAME=ynf-sandbox`, `SANDBOX_FACTORY=ynf-sandbox-factory` and
`SANDBOX_VISIBILITY=private`. Terraform keeps its state in a file on your machine, so no cloud
account is involved. (`SANDBOX_STATE=s3`, with a bucket, region and AWS profile, keeps it in S3
instead, for a sandbox several people share.)

```bash
make -C sandbox up
gh issue list -R <you>/ynf-sandbox
```

Expected: sixteen open issues, among them:

```text
internal/format is not gofmt-clean, started by hand
internal/format is not gofmt-clean, slowly
internal/format is not gofmt-clean
Bump the Go toolchain
Unchecked errors in internal/store
...
```

Each issue describes one problem in the code, and its labels say what kind of work it is
(`ynf:fmt`, `ynf:lint`, `ynf:deps` and others) and which package it's about
(`pkg:internal/format`). This track only works the `gofmt` ones, and one `reclaim` one in lesson 6.
The lint and docs ones are for agents, in the factory track. The rest are fixtures for the
sandbox's acceptance test, `make e2e`, which you don't need here.

## The tracker stand-in

The factory's configuration also declares a tracker that isn't GitHub, standing in for JIRA. ynf
reaches it through an MCP server, the same way it would reach a real one. For the sandbox, that
server is a small program in the clone that keeps its tickets in a JSON file. Build it and give it
one ticket now, so everything ynf checks is in place. Lesson 7 is about using it.

```bash
mkdir -p ~/ynf-tutorial
go build -C sandbox/e2e -o ~/ynf-tutorial/ynf-sandbox-tracker ./tracker
export PATH="$HOME/ynf-tutorial:$PATH"
export YNF_SANDBOX_TRACKER_DATA=~/ynf-tutorial/tickets.json
cat > ~/ynf-tutorial/tickets.json <<'EOF'
{"SBX-1": {"title": "internal/format is not gofmt-clean, from the tracker",
           "body": "`internal/format` is not `gofmt`-clean.", "labels": ["pkg:internal/format"],
           "status": "open", "repo": "github.com/<you>/ynf-sandbox"}}
EOF
```

Replace `<you>` in the file too. Keep the two `export` lines for every terminal you use in this
track.

## Write the config

ynf's own config says where its work comes from and where it keeps its state. Put it in the
tutorial folder and point ynf at it:

```bash
cat > ~/ynf-tutorial/config.yaml <<'EOF'
# yaml-language-server: $schema=https://eyelock.github.io/ynf/schema/config.schema.json
version: 1
factory: { repo: <you>/ynf-sandbox-factory }
lease: { ttl: 30s, heartbeat: 10s }
poll: { ci: 15s, review: 1m }
memory: { provider: none }
EOF
export YNF_CONFIG=~/ynf-tutorial/config.yaml
```

Each line, in order:

- **`factory.repo`** names the configuration repository. ynf reads which repositories to work on
  from it, rather than from this file. A factory's setup lives in git, where it's reviewed like any
  other change.
- **`lease`** is how long ynf's claim on an item lasts without being renewed, and how often it
  renews it. The defaults are 90 and 30 seconds; these short ones make lesson 6 quicker.
- **`poll`** is how often ynf checks a pull request's CI, and its review. Shorter than the
  defaults, so you wait less.
- **`memory: {provider: none}`** switches memory off. Without it, ynf would use ynm if it found it
  on your `PATH` and a store, a `.ynm/` or `~/.ynm/`, to write to. This track doesn't need memory,
  so it says so.

State goes to `state.db` beside the config, and repository clones and run folders to `work/`.

## Doctor

```bash
ynf doctor
```

Expected:

```text
ok    config                   /Users/you/ynf-tutorial/config.yaml
ok    store                    sqlite://state.db
ok    factory                  <you>/ynf-sandbox-factory at 998570c (.agents/factory)
ok    repos                    <you>/ynf-sandbox
ok    lanes <you>/ynf-sandbox .agents/factory on main at 43e2224: deps, detect, doc-drift, fix-ci, gofmt, lint-paydown, outage, reclaim, relaxed, spool, spool-flood, spool-image
ok    forge default            github.com: reached <you>/ynf-sandbox
ok    tracker tracker          tracker.ynf-sandbox.invalid: its server has the tools it is configured to call
ok    git                      git version 2.54.0
ok    docker                   27.4.0
--    ynh                      not found or not working (exec: "ynh": executable file not found in $PATH)
--    ynm                      not found or not working (exec: "ynm": executable file not found in $PATH)
--    ynr                      not found or not working (ynr: exec: "ynr": executable file not found in $PATH)
ok    memory                   off: memory.provider is none
```

### What just happened

`doctor` walked everything ynf depends on, in the order it depends on it:

- **The configuration repository**, read at a commit (`998570c`). Every decision ynf makes later
  records the commit its configuration was read at.
- **The repositories it enrols:** one, your sandbox.
- **That repository's lanes,** read from its default branch at a commit (`43e2224`). Lanes are the
  rules for what ynf does with a ticket. There are twelve; lesson 2 is about them.
- **The forge,** GitHub, reached with your token.
- **The tracker,** by starting its MCP server and checking it has every tool the configuration
  names.
- **The tools on this machine.** ynh, ynm and ynr are marked `--`, not `FAIL`: they're optional. If
  you have them installed, those lines say `ok` with their versions; that's fine too. The `memory`
  line says memory is off because your config says so. (A line for the memory queue appears only
  when memory writes are waiting to be sent, which this track never has.)

Docker is needed because the sandbox's lanes run in containers. If it weren't running, its line
would say `FAIL` and `doctor` would exit non-zero.

## Forges and trackers

`doctor` checks everything at once. These two look at one kind of connection each:

```bash
ynf forges
ynf trackers
```

Expected:

```text
ok    default      github   github.com                       reached <you>/ynf-sandbox
```

```text
ok    default      github   github.com                       its forge's issues
ok    tracker      mcp      tracker.ynf-sandbox.invalid      its server has the tools it is configured to call
```

A **forge** is where code lives: repositories, branches, pull requests and checks. A **tracker** is
where work is described: tickets. GitHub is both, which is why it appears twice. The tracker named
`tracker` is the stand-in, at the made-up host `tracker.ynf-sandbox.invalid`; a real one would be
your JIRA site. Its tickets are written `tracker/SBX-1`: the tracker's name, then the ticket's key.

## How each lane runs

```bash
ynf harness
```

Expected:

```text
<you>/ynf-sandbox
  ok    deps (off): originate lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    detect: originate lane, command (ynh not found) on docker
        a command, not a harness
  ok    doc-drift: originate lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    fix-ci: adopt lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    gofmt: originate lane, command on docker
        a command, not a harness
  ok    lint-paydown: originate lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    outage: originate lane, command on docker
        a command, not a harness
  ok    reclaim: originate lane, command on docker
        a command, not a harness
  ok    relaxed: originate lane, ynh on docker
        harness ., carried in the repository at ., read when a run checks it out
  ok    spool: originate lane, command on docker
        a command, not a harness
  ok    spool-flood: originate lane, command on docker
        a command, not a harness
  ok    spool-image: originate lane, command on docker
        a command, not a harness
```

Each lane runs work with a **runner**, on an **executor**. Two runners appear here:

- **`command`:** a fixed command, such as `gofmt -w ./internal/format`. That's this track.
- **`ynh`:** an agent, run by ynh with a harness of instructions and checks. That's the factory
  track.

All twelve run on the `docker` executor: in a container, with a fresh copy of the repository, and no
network beyond what the lane allows.

`detect` names no runner. It has both a `ynh` block and a `command` block, and ynf chooses: ynh when
it finds a usable one on your `PATH`, otherwise the command. With no ynh here it says `command (ynh
not found)`. If you do have ynh installed, it says `ynh (detected <version>)` instead, and the lane
would run an agent, which is why it is paused below.

## Keep the agents out of this track

Three lanes run agents: `doc-drift`, `fix-ci` and `lint-paydown`. A fourth, `detect`, runs one when
ynh is detected on your machine. A fifth, `relaxed`, is refused before it starts one (the sandbox
README says why). Pause the four that can run one, so nothing in this track starts an agent even if
it finds their tickets. Pause `reclaim` too until lesson 6, which is about it:

```bash
ynf pause lint-paydown --reason "the ynf track runs no agents"
ynf pause doc-drift --reason "the ynf track runs no agents"
ynf pause fix-ci --reason "the ynf track runs no agents"
ynf pause detect --reason "the ynf track runs no agents"
ynf pause reclaim --reason "until lesson 6"
```

Expected, one line each:

```text
<you>/ynf-sandbox/lint-paydown: paused (the ynf track runs no agents)
<you>/ynf-sandbox/doc-drift: paused (the ynf track runs no agents)
<you>/ynf-sandbox/fix-ci: paused (the ynf track runs no agents)
<you>/ynf-sandbox/detect: paused (the ynf track runs no agents)
<you>/ynf-sandbox/reclaim: paused (until lesson 6)
```

A paused lane still notices its tickets, but nothing new starts in it. The reason is required:
pausing is recorded, with who did it and why, and `ynf stats` shows it. Another lane with agent
work, `deps`, is already off, as `harness` showed: the sandbox switches it off in its own lanes.
Lesson 2 shows how.

That leaves `outage`, `spool`, `spool-image` and `spool-flood`. They are fixtures for `make e2e`, the
sandbox's acceptance test: command lanes, with no model, that exercise a failing run and a run's
own telemetry. This track never starts them. Every sweep in it names `--lane gofmt` (or `reclaim`),
so they stay out of the way, and `spool-image` needs an image that only `make e2e` builds.

## What you know now

- ynf works on repositories a **configuration repository** enrols, under **lanes** read from git
  at a commit.
- It talks to **forges** for code and **trackers** for tickets, and checks both before relying on
  them.
- Each lane runs work with a **runner** on an **executor**. In this track every runner is a command
  in a container.
- ynh, ynm and ynr are optional.

Next: [2. Lanes](02-lanes.md).
