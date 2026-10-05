# Test the factory against the sandbox

The sandbox is a pair of repositories of your own, `<owner>/ynf-sandbox` and
`<owner>/ynf-sandbox-factory`, built from
[`sandbox/`](https://github.com/eyelock/ynf/tree/main/sandbox) with planted problems and an expected
outcome for every issue and pull request. Three commands use it, and they test different things.

## Set it up once

```bash
cd sandbox
cp sandbox.env.example sandbox.env    # set SANDBOX_OWNER: your GitHub user or organisation
gh auth refresh -s delete_repo        # resetting deletes the repositories
make up
```

Terraform keeps its state in a local file unless `sandbox.env` sets `SANDBOX_STATE=s3` with a
bucket, region and `AWS_PROFILE`, so that other machines can reset the same sandbox. Only then is
AWS needed. See [`sandbox/README.md`](https://github.com/eyelock/ynf/tree/main/sandbox#your-settings).

## What each command tests

| Command | Tests | Needs |
|---|---|---|
| `make reset` | nothing: deletes and rebuilds the sandbox | `sandbox.env`, Terraform, a token with `delete_repo`; AWS only for S3 state |
| `make calibrate` | the **fixtures**: each fails before its known fix and passes after | ynh from `develop` (`.agents/harness/`) |
| `make e2e` | the **factory**: ynf runs the fixtures and ends where each expects | Docker |

Run them from `sandbox/`, or `make e2e` and `make calibrate` from the repository root.

## The acceptance test

```bash
make e2e                       # reset, then e2e-only
make e2e-only LANES=gofmt,deps # against the sandbox as it is
```

It builds ynf, runs `ynf sweep --until-settled` for the lanes in `LANES` with a fresh store, and
checks every fixture in those lanes:

- the item ended in the state its `expect.result` names
- a proposal is a draft pull request from `ynf/issue-<n>` whose commit has the `YNF-Item`,
  `YNF-Step` and `YNF-Run` trailers
- every decision replays the same

```
running ynf gofmt, deps
settled in 58s

ok    fmt-format               #6 in_review, draft #11 with trailers, 6 decisions replay the same
ok    deps-bump                #3 ignored, 1 decisions replay the same
```

Without a model key, run the lanes that need no agent: `gofmt` and `deps` (the default). The ynh
lanes run the agent in a container built on the sandbox's agent base image:

```bash
make agent-image                                   # once, and after ynh changes
ANTHROPIC_API_KEY=... make e2e-only LANES=lint-paydown,doc-drift
```

With the `gofmt` lane, `make e2e` ends with [shadow mode](measure-a-lane-with-shadow-mode.md): e2e
merges a human fix for the `fmt-format` issue into the sandbox's `main`, which closes it, then runs
`ynf shadow run gofmt` on it, grades the attempt with the scripted form, reads the report, and checks
that nothing outward changed (the issue's comments and labels, the pull requests, the branches, ynf's
items and its stats). It waits for the sandbox's required checks on the human fix, so it adds a few
minutes. `make e2e SHADOW=false` leaves it out. `make e2e-only` leaves it out by default, because the
first run leaves `fmt-format` fixed and closed and a second could not repeat it; `make e2e-only
SHADOW=true` adds it to a sandbox just reset.

Running `e2e-only` twice against the same sandbox is safe: ynf finds the pull request it already
opened and reuses it.
