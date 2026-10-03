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

## Where the file is found

The first of these that has `config.yaml`; any later one is shadowed and `ynf doctor` says so:

1. `~/.agents/factory/`
2. `~/.ynh/ynf/`
3. `~/.ynm/ynf/`
4. `~/.ynf/`
