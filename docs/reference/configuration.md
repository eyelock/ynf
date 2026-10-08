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
| `memory.provider` | detected | `ynm`, or `none` to switch memory off. Without a `memory` block, ynf uses ynm when it is on PATH **and** a store is present (ADR-012): a `.ynm/` in the directory ynm runs from (`memory.cwd`, else where ynf runs) or any directory above it, or the user's own store, `$YNM_HOME` when that is set (ynm keeps its store there instead of in `~/.ynm/`) and `~/.ynm/` when it is not. With the binary and no store, memory stays off and `ynf doctor` says why on its `memory` line. Setting `provider: ynm` turns memory on without that check. Finding the binary is PATH only. ynf writes what only it can see: one `ynf.failure.v1` record per occurrence of a failure signature (steps are not written; ynf's own store is the run history). It never puts memory into an agent's task: a harness that wants memory connects its agent to ynm itself (ADR-008). |
| `memory.namespace` | `factory/{repo}` | Where a repository's memories go; `{repo}` is `host/owner/name`, so the same `owner/name` on two forges never share one. |
| `memory.cwd` | where ynf runs | With `transport: cli`, the directory ynm runs as if from, which decides its store: your own on a laptop. |
| `memory.transport` | `cli` | `cli`, the ynm CLI; or `http`, a hosted ynm's MCP endpoint, the shared store for a pool of workers or CI, where many writers go through one server (ADR-008). |
| `memory.endpoint` | none | The hosted ynm's MCP endpoint, for `transport: http`. |
| `memory.token_env` | none | The variable holding the bearer token for `transport: http`, such as a machine token from your identity provider's client-credentials grant; its subject is the writer in ynm's audit log. A shared static token names no one, so every worker's writes are recorded as `token:static`; records still land in ynf's namespace. ynf refuses to start without it. |
| `memory.level` | `personal`; `distributed` over http | The level ynf writes at. A shared store keeps nothing at the personal level, so writes to one say `distributed`. |
| `telemetry.spool` | none | The spool root (ADR-011), relative to the file's folder. ynf writes its own telemetry to `factory/` under it, gives each run a folder at `runs/<run id>/` and a manifest at `manifests/<run id>.json`, and starts each run with `YNR_SPOOL` set to its own folder. Put it on a filesystem of its own, apart from `work_dir` and the store, such as a tmpfs (ADR-007). Without it, ynf writes where the environment says, as [See ynf in OpenTelemetry](../how-to/see-ynf-in-opentelemetry.md) describes. |
| `telemetry.run_quota` | `64MiB` | How large a run's folder may grow (`KiB`, `MiB` or `GiB`). Where the host allows it the folder is a volume of its own of that size, and a write past it fails, and a volume left by a ynf that was killed is cleaned up by the next job at its start, which keeps its spool files in the run capture first; elsewhere ynf removes the largest files from it, which bounds a flood but is not a hard limit. ADR-007 says which executor and host gets which. |
| `telemetry.collector.enabled` | `false` | Start `ynr serve` on the spool for the length of a factory job (below). Off by default, and never turned on because `ynr` was found (ADR-012). Needs `telemetry.spool`, `collector.id` and an upstream. |
| `telemetry.collector.id` | required when enabled | The collector's identity: the runner pool or the host the job runs on, stable from one job to the next. Never the job's own id. Lower-case letters, digits, `.`, `_` and `-`, as `ynr serve` asks. |
| `telemetry.collector.instance` | none | The job within the pool, such as a CI run id (a string: quote a number). It is recorded as data on what the collector ships, not as identity. |
| `telemetry.collector.upstream` | the operator's endpoint | The OTLP/HTTP endpoint `ynr serve` ships to. When it is not set, ynf uses `OTEL_EXPORTER_OTLP_ENDPOINT` (or a signal's own endpoint, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` and the others, with its `/v1/<signal>` path taken off), then `YNR_UPSTREAM`. `ynr serve` needs one until it has an object store of its own, so a collector that is enabled with none fails at config load, saying so. The endpoint's headers are not passed on. |
| `telemetry.collector.archive` | `30s` | How long `ynr serve` has at a job's end, after `SIGTERM`, to ship what is left. |

A write ynm cannot take is not lost and does not stop a step: it waits in ynf's own store, up to
1000 records (then the oldest are dropped, with a warning), and is sent, oldest first, before the next
write and on every sweep. `ynf doctor` reports what is waiting.

What ynf writes to memory, and why it never decides anything with it, is in
[Learning from failure](../explanation/learning-from-failure.md): one
[`ynf.failure.v1`](../schema/memory/ynf.failure.v1.schema.json) record per occurrence of a failure
signature, tagged `ynf.failure.v1` and `occurrence`. A record carries what the run reported about the
failure, so reflection can explain it: the outcome, exit code and the cap that bound the run, the
failing sensors or CI checks by name, the harness, and an excerpt of the run's own message, scrubbed
of secrets and cut to 1 KiB. It holds no ticket text and no prompt. Each run's model, effort, turns, tokens and cost stay in ynf's
own store, where `ynf stats` reads them.

## Token scopes

ynf reads `GITHUB_TOKEN` (or the variable `github.token_env` names, or `gh auth token`). With a
classic token:

| Scope | Needed for |
|---|---|
| `repo` | Everything ynf does: searching and reading issues, pull requests, checks and files, labelling and commenting, pushing its branches and opening pull requests. |
| `workflow` | Only to push a change to a workflow file under `.github/workflows/`. GitHub refuses such a push without it. ynf's diff gate refuses workflow changes in a lane's pull request anyway, so a factory does not need it; the sandbox's setup does, to push its own workflows. |
| `delete_repo` | Only the sandbox's clean-up (`make -C sandbox reset` and `destroy`), which deletes the repositories it created. ynf itself never deletes one. |

Fine-grained tokens have not been tried. From the calls ynf makes, one would need these
repository permissions, which is a reading of the code and not a tested list:

| Permission | Why |
|---|---|
| Metadata: read | Repositories and branches. |
| Contents: read and write | Reading lanes and files; pushing ynf's branches. |
| Pull requests: read and write | Opening, finding and reading pull requests and their reviews. |
| Issues: read and write | Reading tickets, labels, comments and search; labelling and commenting. |
| Checks: read | Check runs on a head commit. |
| Commit statuses: read | Commit statuses on a head commit. |
| Administration: read | Branch protection's required checks. Optional: without it ynf reads the rulesets, and when neither can be read every check gates a pull request (`ynf doctor` says so). |

## A factory job with the collector on

With `telemetry.collector.enabled`, a factory job starts `ynr serve` on the spool and stops it at
its end (ADR-011). The commands that count as a factory job are the ones that do factory work for
as long as they run: `ynf sweep`, `ynf serve`, `ynf handle` and `ynf shadow run`. `ynf start` is a
person's own, attended command and starts no collector; nor does a command that only reads or
records.

```yaml
telemetry:
  spool: /var/spool/ynf        # a tmpfs or a volume of its own
  run_quota: 64MiB
  collector:
    enabled: true
    id: gha-linux-pool         # the runner pool, not this job
    instance: ${CI_RUN_ID}     # written by whatever renders the file
    upstream: https://otel.example.com
```

The job runs `ynr serve --spool <spool> --collector-id <id> [--collector-instance <instance>]
--upstream <endpoint>`. With the collector on, **the spool wins**: ynf writes its own telemetry to
`factory/` whatever `OTEL_EXPORTER_OTLP_*` says, and every run starts without those variables, so
the operator's endpoint is honoured at the edge by `ynr serve`'s upstream and no run needs a way
to reach it. At the job's end ynf waits for `ynr serve` to ship and delete the closed files left in
the spool, stops it with `SIGTERM`, gives it the rest of the archive time to ship, and then copies any spool file still in `runs/` or `factory/` into the run capture (ADR-010):
into the step's `spool/` folder for a run's own, and `<work_dir>/spool-capture/<time>/` for the
rest.

If `ynr` is missing, or does not start, or ends while the job runs, the job logs that plainly and
carries on: telemetry never fails the factory. `ynf doctor` says the same. A job killed with
`SIGKILL` leaves its `ynr serve` running; the container a job runs in ends it.

## Detecting ynr

`ynr` is an optional tool, found the way ynh is: `ynr` on the `PATH` ynf is given, and nowhere
else, asked with `ynr info --format json`. `ynf doctor` shows what it found. Finding it starts nothing: only
`telemetry.collector.enabled` starts `ynr serve`, and only for a factory job.

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

## Detecting ynh

A lane that omits `run.runner` runs as ynh when it has a `ynh` block and ynh is detected on the
host, else as its `command` block (ADR-012); with neither it is refused with `operator_error`. ynh
is detected when `ynh version --format json` answers with capabilities 0.9.0 or later, using
`ynh` on the `PATH` ynf is given, and nowhere else. It is checked once per process, always on the
host, including for a lane whose run is in a published `run.image`. A lane that names `runner: ynh`
never falls back: where ynh runs on the host and is missing, the run is refused. Each run record
says `runner_detected` and the ynh version, and `ynf harness` and `ynf lanes show` say what an
unnamed runner resolves to here.

A lane with both blocks can name each one's image: `run.command.image` is used when it resolves to the
command runner and wins over `run.image`; when it resolves to ynh it is ignored, and ynh uses
`run.image` or the image it builds from `ynh.base`.

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
