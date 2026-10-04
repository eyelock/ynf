# Configuration

`config.yaml`, validated against
[`docs/schema/config.schema.json`](../schema/config.schema.json). Relative paths resolve against
the folder the file is in.

| Key | Default | Meaning |
|---|---|---|
| `version` | required | `1`. |
| `repos` | one of `repos` or `factory` | Enrolled repositories, `owner/name`. ynf reads each one's lanes from its default branch. |
| `factory.repo` | one of `repos` or `factory` | The factory's configuration repository (ADR-006), `owner/name` or `host/owner/name`. Its `factory.yaml` enrols repositories instead of `repos`, and its `lanes.yaml` lies under every enrolled repository's lanes, below. |
| `images.build` | `true` | Build an agent image with `ynh image` for a lane that names no published one (`run.image`). `false` on a deployed factory, so it only runs pinned, published images. |
| `store` | `sqlite://state.db` | Where state lives (ADR-004): `sqlite://<path>`, or `s3://bucket/prefix?region=…` for state that outlives the process (CI, the hosted service), with credentials from the AWS chain; `endpoint=` and `path_style=true` point it at MinIO or another S3. `dynamodb://` is not built yet. |
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

## The configuration repository

A factory's configuration lives outside the repositories it works on, in a repository of its own,
read from its default branch at a resolved commit (ADR-006). In its factory folder
(`.agents/factory/`, or a fallback):

- `factory.yaml`, validated against [`docs/schema/factory.schema.json`](../schema/factory.schema.json):
  `version: 1`, the enrolled `repos`, and the `trackers` and `forges` the factory works with.
  A forge besides the default one, such as a GitHub Enterprise Server, is declared by name with
  `provider: github`, its `url` and the `token_env` holding ynf's token for it; its repositories
  are enrolled as `host/owner/name`, and everything about them (searches, clones, pushes, pull
  requests, labels, webhooks) goes to that forge with that token.
  A tracker that is not a forge, such as JIRA Cloud or Data Center, is declared by name with
  `provider: mcp`: its `site`, its MCP `server` (a `command` started with only PATH, HOME and
  the variables in its `env`, or a `url` with a `token_env` bearer token), the tool and arguments
  for `get`, `comment` and `label`, CEL `fields` that read the get tool's result (`title`,
  `body`, `labels`, `status`, and `repo` when the ticket names its repository in a structured
  field), and `closed_when`. ynf calls the tools directly, with no model, using its own
  credentials. A reference may use the tracker's name, `jira/PLAT-881`; the item is stored by the
  tracker's host. A ticket that names its repository must agree with `--repo`.
  These are declared only here: they carry credentials and decide which repositories ynf
  touches, so a target repository can never declare or change them.
- `lanes.yaml`, optional: lanes and defaults for every enrolled repository. A target repository's
  own `lanes.yaml` lies over it key by key and wins: maps merge, anything else is replaced, so a
  repository can change one value, add lanes, or turn one off with `enabled: false`, and a
  repository with no `lanes.yaml` of its own gets these alone. The merged result is what is
  validated.

Every decision records the commits both layers were read at, and `ynf lanes show` gives the
source of every value, `config@<sha>` or `repo@<sha>`.

## Where the file is found

The first of these that has `config.yaml`; any later one is shadowed and `ynf doctor` says so:

1. `~/.agents/factory/`
2. `~/.ynh/ynf/`
3. `~/.ynm/ynf/`
4. `~/.ynf/`
