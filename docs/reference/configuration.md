# Configuration

`config.yaml`, validated against
[`docs/schema/config.schema.json`](../schema/config.schema.json). Relative paths resolve against
the folder the file is in.

| Key | Default | Meaning |
|---|---|---|
| `version` | required | `1`. |
| `repos` | required | Enrolled repositories, `owner/name`. ynf reads each one's lanes from its default branch. |
| `store` | `sqlite://state.db` | Where state lives. `s3://` and `dynamodb://` come with the hosted service (ADR-004). |
| `work_dir` | `work` | Repository mirrors and per-step worktrees and run folders. |
| `owner` | `ynf@<host>/<pid>` | This instance's name in leases. |
| `github.token_env` | `GITHUB_TOKEN` | The variable holding the forge token; `gh auth token` if unset. |
| `github.author` | `ynf <ynf@users.noreply.github.com>` | The git author of ynf's commits: `{name, email}`. |
| `lease.ttl` | `90s` | How long a claim lasts without renewal (ADR-005). |
| `lease.heartbeat` | `30s` | How often a holder renews. Must be shorter than `ttl`. |
| `poll.ci` | `30s` | How often a proposed item's checks are probed. |
| `poll.review` | `5m` | How often an item in review is probed for merge, close or review. |
| `memory.provider` | detected | `ynm`, or `none` to switch memory off. Without a `memory` block, ynf uses ynm when it is on PATH (ADR-012). |
| `memory.namespace` | `factory/{repo}` | Where a repository's memories go; `{repo}` is `owner/name`. |
| `memory.context_budget_tokens` | `1000` | How much remembered context is added to a run's task. |
| `memory.cwd` | where ynf runs | The directory ynm runs as if from, which decides its store. |

What ynf writes to memory, and why it never decides anything with it, is in
[Learning from failure](../explanation/learning-from-failure.md); the data shapes are
[`ynf.step.v1`](../schema/memory/ynf.step.v1.schema.json) and
[`ynf.failure.v1`](../schema/memory/ynf.failure.v1.schema.json).

## Where the file is found

The first of these that has `config.yaml`; any later one is shadowed and `ynf doctor` says so:

1. `~/.agents/factory/`
2. `~/.ynh/ynf/`
3. `~/.ynm/ynf/`
4. `~/.ynf/`
