# Test the factory against the sandbox

The sandbox, `eyelock/ynf-sandbox`, is a private repository built from
[`sandbox/`](https://github.com/eyelock/ynf/tree/main/sandbox) with planted problems and an expected
outcome for every issue and pull request. Three commands use it, and they test different things.

| Command | Tests | Needs |
|---|---|---|
| `make reset` | nothing: deletes and rebuilds the sandbox | the `ynf-terraform` AWS profile, a token with `delete_repo` |
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

Slice 1a covers the lanes that need no agent and no egress: `gofmt` and `deps`. The others wait
for the ynh runner in a container and the egress proxy.

Running `e2e-only` twice against the same sandbox is safe: ynf finds the pull request it already
opened and reuses it.
