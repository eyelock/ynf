# Run ynf locally

Build ynf, point it at a repository that has lanes, and let it work until everything it can do
without a human is done.

## What you need

- Go 1.26, git, and Docker running: unattended lanes run in a container (ADR-007).
- A GitHub token that can read the repository and push branches to it: `GITHUB_TOKEN`, or a
  `gh auth login` session, which ynf falls back to.
- A repository with `.agents/factory/lanes.yaml` on its default branch. The sandbox has one
  ([Test the factory against the sandbox](test-against-the-sandbox.md)).

## Install

```bash
brew install eyelock/tap/ynf
```

The formula installs `ynf`, and keeps the static linux builds the docker executor runs as its
egress proxy in its `libexec`, off your PATH.

Or build from source:

```bash
make build            # bin/ynf, and bin/ynf-linux-{amd64,arm64} for the egress proxy
make install          # all three into ~/.ynf/bin; INSTALL_DIR=<dir> for elsewhere
export PATH="$HOME/.ynf/bin:$PATH"   # in your shell profile, as for ~/.ynh/bin and ~/.ynm/bin
```

## Configure

ynf reads `config.yaml` from the first of `~/.agents/factory/`, `~/.ynh/ynf/`, `~/.ynm/ynf/`,
`~/.ynf/`, or the file given with `--config`. The smallest one names the repositories to watch:

```yaml
# yaml-language-server: $schema=https://eyelock.github.io/ynf/schema/config.schema.json
version: 1
repos: [eyelock/ynf-sandbox]
```

State goes to `state.db` and work to `work/`, both beside the config file. Every key is in
[Configuration](../reference/configuration.md).

```bash
ynf doctor            # config, store, lanes, forges, trackers, and git, docker, ynh, ynm
ynf harness           # how each lane runs, and the harness it is held to
```

## Run

```bash
ynf sweep                                     # one pass: searches, then everything due
ynf sweep --until-settled --lane gofmt        # keep going until only a human can move things
ynf serve                                     # forever, every minute
```

A sweep runs each lane's searches, tracks every new ticket as an item, and steps it: decide, run
the lane's runner in a container on a fresh worktree, gate the diff, commit with ynf's trailers,
push `ynf/issue-<n>`, open a draft pull request, then watch CI. An item is settled once it is in
review, escalated, quarantined, ignored, done or closed.

## Inspect

```bash
ynf items ls
ynf items log eyelock/ynf-sandbox#6          # every decision, run and action, in order
ynf --format json items show eyelock/ynf-sandbox#6
```

Each run keeps its task, stdout and stderr in `work/steps/<item>/<run>/run/`.

## Replay

```bash
ynf replay eyelock/ynf-sandbox#6                         # every decision recomputes the same
ynf replay eyelock/ynf-sandbox#6 --policy new-lanes.yaml # what a policy change would have done
```

Replay exits `32` when a decision differs, so it can gate a pull request that changes lanes.

## When a human is needed

An escalated or quarantined item says why on its ticket. Once the cause is fixed:

```bash
ynf items retry eyelock/ynf-sandbox#6        # back to ready; the next sweep runs it
ynf items release eyelock/ynf-sandbox#6      # clear a lease left by an instance that died
```
