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
| `executor` | none | `inline` declares this instance runs inside containment the operator provides: the factory image run as a job (ADR-007, ADR-009). Every run then starts ynh as a process beside ynf, as `inline_user`, using the harness installed in the image; the run's folders and the repository mirror are handed to that user for the run and taken back after. The job runner provides the containment: run the image as root with `--cap-drop ALL --cap-add SETUID --cap-add SETGID --cap-add CHOWN --security-opt no-new-privileges`, and a network policy allowing the forge, the model API and the lane's hosts. Never set it outside such a container. |
| `inline_user` | `ynh` | The user inline runs run as. It must not be the user ynf runs as, so the run cannot read ynf's environment or token. |
| `store` | `sqlite://state.db` | Where state lives (ADR-004): `sqlite://<path>`, or `s3://bucket/prefix?region=…` for state that outlives the process (CI, the hosted service), with credentials from the AWS chain; `endpoint=` and `path_style=true` point it at MinIO or another S3. `dynamodb://` is not built yet. |
| `work_dir` | `work` | Repository mirrors and per-step worktrees and run folders. |
| `owner` | `ynf@<host>/<pid>` | This instance's name in leases. |
| `github.token_env` | `GITHUB_TOKEN` | The variable holding the forge token; `gh auth token` if unset. |
| `github.author` | `ynf <ynf@users.noreply.github.com>` | The git author of ynf's commits: `{name, email}`. |
| `lease.ttl` | `90s` | How long a claim lasts without renewal (ADR-005). |
| `lease.heartbeat` | `30s` | How often a holder renews. Must be shorter than `ttl`. |
| `poll.ci` | `30s` | How often a proposed item's checks are probed. |
| `poll.review` | `5m` | How often an item in review is probed for merge, close or review. |
| `memory.provider` | detected | `ynm`, or `none` to switch memory off. Without a `memory` block, ynf uses ynm when it is on PATH (ADR-012). ynf writes what only it can see (each step's outcome, and failures recurring across runs) and never puts memory into an agent's task: a harness that wants memory connects its agent to ynm itself (ADR-008). |
| `memory.namespace` | `factory/{repo}` | Where a repository's memories go; `{repo}` is `host/owner/name`, so the same `owner/name` on two forges never share one. |
| `memory.cwd` | where ynf runs | With `transport: cli`, the directory ynm runs as if from, which decides its store: your own on a laptop. |
| `memory.transport` | `cli` | `cli`, the ynm CLI; or `http`, a hosted ynm's MCP endpoint, the shared store for a pool of workers or CI, where many writers go through one server (ADR-008). |
| `memory.endpoint` | none | The hosted ynm's MCP endpoint, for `transport: http`. |
| `memory.token_env` | none | The variable holding the bearer token for `transport: http`, such as a machine token from your identity provider's client-credentials grant; its subject is the writer in ynm's audit log. ynf refuses to start without it. |
| `memory.level` | `personal`; `distributed` over http | The level ynf writes at. A shared store keeps nothing at the personal level, so writes to one say `distributed`. |

What ynf writes to memory, and why it never decides anything with it, is in
[Learning from failure](../explanation/learning-from-failure.md): one
[`ynf.failure.v1`](../schema/memory/ynf.failure.v1.schema.json) record per occurrence of a failure
signature, tagged `ynf.failure.v1`. Each run's model, effort, turns, tokens and cost stay in ynf's
own store, where `ynf stats` reads them.

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
  `body`, `labels`, `status`, `repo` when the ticket names its repository in a structured
  field, and `comments`, a list of the text of each of the ticket's comments), and `closed_when`. ynf calls the tools directly, with no model, using its own
  credentials. A reference may use the tracker's name, `jira/PLAT-881`; the item is stored by the
  tracker's host. A ticket that names its repository must agree with `--repo`.
  Map `comments` so a retried step never comments twice: ynf posts a comment only if none of
  those texts contains its hidden marker, as it does on GitHub. Without `comments` it cannot
  tell, and a retried step may post the same comment again.
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

## The factory image

`make factory-image` builds ynf's factory image (ADR-009): ynh's image at the version
[`images/factory/versions.env`](../../images/factory/versions.env) pairs, with this checkout's ynf
and that ynm. `YNH_SRC=<ynh checkout>` and `YNM_SRC=<ynm checkout>` build dev versions in instead;
`FACTORY_IMAGE` names it (default `ynf-factory:dev`). A release publishes it as
`ghcr.io/eyelock/ynf-factory:<version>` for amd64 and arm64.

Harness images built on it with `ynh image <harness> --base ynf-factory:<version> --entrypoint
agent` are the factory flavour: run one as a job with `--user root --entrypoint ynf`, the
capabilities above, a config with `executor: inline`, and `ynf start` or `ynf handle`.
`make -C sandbox e2e-factory` is the acceptance test of exactly that.
