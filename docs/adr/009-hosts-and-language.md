# ADR-009: Hosts and language

Status: draft (2026-10-03)
Satisfies: NFR-1, NFR-2, NFR-7, NFR-10

## Context

ynf needs to run three ways: as a daemon on a developer's machine or a box they run, as a hosted
service, and inside CI with the CI system as the runtime. A design that assumes a long-running
process cannot run in CI; one that assumes CI cannot hold a webhook endpoint.

## Decision

**The core is one idempotent operation:** `step(event)` claims the item, probes, decides, acts,
records and releases. It holds nothing in memory between calls (NFR-2). The hosts are thin
wrappers that differ only in which adapters they plug in:

| Port | Daemon | Hosted service | CI-native |
|---|---|---|---|
| Intake | searches, plus a local webhook listener behind a tunnel or relay | HTTP webhooks and topic consumers | workflow `on:` triggers and `schedule:` |
| Store | SQLite | S3 or DynamoDB | S3 or DynamoDB |
| Executor | `docker` (`process` for interactive and shadow) | `docker`, `ecs`, `k8s-job` | `ci-inline` |
| Clock | in-process scheduler | queue delay and cron | a cron workflow running `ynf sweep` |

```
ynf serve                          # daemon and hosted service
ynf step --event "$GITHUB_EVENT_PATH"   # CI-native: one event, one step
ynf sweep                          # cron hosts: run searches and due timers once
```

**Go**, for a single static binary per platform and the same release path as ynh: GoReleaser,
the `eyelock/tap` Homebrew formula and a container image. It also gives cel-go (the reference CEL
implementation), the AWS SDK v2 (S3 conditional writes, DynamoDB conditions) and the same
`jsonschema/v6` validator ynh uses.

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

`ynf doctor` reports which folder each level resolved to. The daemon's SQLite state defaults to
`<home folder>/state.db`.

## Alternatives

- **TypeScript**, matching ynm. Octokit is the better GitHub client, and ynm's S3 provider is a
  pattern to copy. Rejected for CEL maturity, the binary story, and consistency with ynh's
  exit-code and JSON contracts.
- **CI-native only.** Simple, but no low-latency path and no way to hold leases across long runs
  without a store anyway.

## Consequences

- The `step` contract is the integration test boundary: every host runs the same conformance
  suite of event, state and expected decision fixtures.

## Open questions

- Hosted service on Lambda as well, as ynm does? `step` fits Lambda; the `docker` executor does
  not, so a Lambda host would dispatch runs to ECS.

## History

- 2026-10-03: drafted.
