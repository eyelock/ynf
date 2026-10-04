# ADR-009: Hosts and language

Status: draft (2026-10-03)
Satisfies: NFR-1, NFR-2, NFR-7, NFR-10

## Context

ynf has to run on a developer's machine without Docker, on a pool of always-on workers, inside a
job runner that takes one container image per job, and inside CI. A design that assumes a
long-running process cannot run in CI; one that assumes CI cannot hold a webhook endpoint; one that
assumes Docker cannot run on every developer's machine.

ynf, ynh and ynm are each single binaries that keep no state of their own: ynf's lives in its
store, ynm's in a git remote or a hosted store, and ynh's runs in folders the caller gives it.

## Decision

**The core is one idempotent operation:** `step(event)` claims the item, probes, decides, acts,
records and releases. It holds nothing in memory between calls (NFR-2). The hosts are thin
wrappers that differ only in which adapters they plug in:

| Port | Developer machine | Worker pool | Job runner | CI-native |
|---|---|---|---|---|
| Intake | `ynf start`, searches | `POST /start`, webhooks behind a load balancer, topics, searches | `ynf start` or `ynf handle` as the job's command | workflow `on:` triggers and `schedule:` |
| Store | SQLite | S3 or DynamoDB | S3 or DynamoDB | S3 or DynamoDB |
| Executor (ADR-007) | `process` for attended work, `docker` | `docker` | `inline` | `inline` |
| Memory (ADR-008) | the developer's ynm store | hosted ynm over HTTP | hosted ynm over HTTP | hosted ynm over HTTP |
| Clock | in-process scheduler | in-process scheduler on every worker, deduplicated by leases | none: the job is one piece of work | a cron workflow running `ynf sweep` |

```
ynf start <ref> | --prompt …      # an instruction: one item, now (developer, job runner)
ynf serve                          # an always-on worker, alone or one of a pool
ynf handle --github-event <file>   # one event, one step (CI-native, job runner)
ynf sweep                          # cron hosts: run searches and due timers once
```

**A pool is just several workers sharing a store.** Each runs `ynf serve` and starts ynh instances
beside it. Leases (ADR-005) make it safe for any worker to take any trigger, and an item whose
worker dies is restarted by another within one lease TTL. A worker needs no state of its own, so
workers are added and removed freely.

**Go**, for a single static binary per platform and the same release path as ynh: GoReleaser,
the `eyelock/tap` Homebrew formula and container images. It also gives cel-go (the reference CEL
implementation), the AWS SDK v2 (S3 conditional writes, DynamoDB conditions) and the same
`jsonschema/v6` validator ynh uses.

**ynf publishes the factory image.** ynf is the layer that brings the tools together, so it owns
the image that carries them, `ghcr.io/eyelock/ynf-factory:<ynf version>`:

```
ghcr.io/eyelock/ynh:<pinned>        ynh, git, Node and the vendor CLIs    (published by ynh)
  └─ ynf-factory:<version>          + ynf, + ynm (its Node build)          (published by ynf)
```

The ynh and ynm versions it takes are pinned in one file in this repository, and a change to
either is an ordinary pull request that runs the acceptance tests against the composed image
before it is published. The release checks that the image's ynh reports the capabilities ynf
needs (ADR-012). ynh and ynm release on their own schedules and know nothing of this image.

**Two flavours of every harness image,** built the same way from the same harness and differing
only in their base, with `ynh image <harness> --base <image>`:

| Flavour | Base | For |
|---|---|---|
| Developer | ynh's image | people running the harness directly |
| Factory | ynf's factory image | a job runner, which runs it as one job with `ynf start` or `ynf handle` |

Layers are shared through the registry, so the factory flavour costs one small layer per harness.

**An instance's settings and the factory's configuration are separate.** An instance's own
`config.yaml` holds what belongs to that machine: its store, its memory, its credentials by
variable name, and which configuration repository it follows. Enrolment, trackers, forges and
default lanes are the factory's, in its configuration repository (ADR-006), so every worker in a
pool follows one reviewed source.

**Conventions copied from ynh:** `--format json` on every command with one stable object per
invocation, meaningful exit codes, and every `YNF_*` environment variable a fallback for an
explicit flag. Config precedence: defaults, then the home folder, then the repository folder,
then `YNF_*`.

**Configuration lives in `.agents/factory/`.** `.agents/` is the cross-vendor folder agent
tooling already shares: the Agent Skills standard and Codex read `.agents/skills` and
`.agents/plugins`, and ynh reads its manifest from `.agents/harness/` (ynh PR #394). Folders in
`.agents/` are named for the role, not the tool, so ynf's is `factory/`. Fallbacks let a
repository that already has ynh's `.ynh/` (committed baselines) or ynm's `.ynm/` (config) keep
ynf's files beside them instead:

| Level | Lookup order |
|---|---|
| Repository | `.agents/factory/`, then `.ynh/ynf/`, then `.ynm/ynf/`, then `.ynf/` |
| Home | `~/.agents/factory/`, then `~/.ynh/ynf/`, then `~/.ynm/ynf/`, then `~/.ynf/` |

The rules follow ynh's for `.agents/harness/`:

- `.agents/factory/` is canonical. `ynf init` and every command that creates a file write there.
- An existing file is rewritten where it is. Nothing is relocated, and a command that only reads
  never writes, so no rename appears in `git status` from a read.
- When more than one candidate exists at the same level, the first in the order wins and the
  others are **shadowed**: never merged, never read, and reported as an issue by `ynf doctor` and
  `ynf lanes validate`. Merged policy is how a looser value slips in unnoticed; shadowing cannot
  do that.
- In the fallbacks ynf's files sit in a `ynf/` subfolder, never loose in `.ynh/` or `.ynm/`, so
  they cannot collide with files those tools add later.

`ynf doctor` reports which folder each level resolved to. A developer machine's SQLite state defaults to
`<home folder>/state.db`.

## Alternatives

- **One image built by ynh, with ynf and ynm inside.** One fewer image, but every ynh release
  would have to choose ynf and ynm versions, and developers who only want ynh would get the rest.
  Composing them is ynf's job, so the factory image is ynf's.
- **Dispatch every run to CI from a central service.** Kept as a possible executor later. As the
  foundation it puts a network round trip and an artifact hand-off into every run, where a worker
  can run the ynh instance beside itself.
- **TypeScript**, matching ynm. Octokit is the better GitHub client, and ynm's S3 provider is a
  pattern to copy. Rejected for CEL maturity, the binary story, and consistency with ynh's
  exit-code and JSON contracts.
- **CI-native only.** Simple, but no low-latency path and no way to hold leases across long runs
  without a store anyway.

## Consequences

- The `step` contract is the integration test boundary: every host runs the same conformance
  suite of event, state and expected decision fixtures.

## Open questions

- What a job runner can do per job (more than one container, egress restricted to a list) decides
  whether `inline` is needed there, or whether `docker` works inside a job (ADR-007).
- Hosted service on Lambda as well, as ynm does? `step` fits Lambda; the `docker` executor does
  not, so a Lambda host would dispatch runs to ECS.

## History

- 2026-10-03: drafted.
