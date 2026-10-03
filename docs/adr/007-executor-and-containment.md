# ADR-007: Executor and containment

Status: draft (2026-10-03)
Satisfies: FR-7, FR-11, FR-13, FR-14, NFR-4, NFR-5, NFR-6

## Context

ynh is explicit that it "declares, executes and packages. It does not enforce containment", and
that an unattended loop without a container and an egress policy "is an unattended agent with
your credentials on an open network". ynf is the operator that ynh hands containment to.

Ticket and issue text is a prompt-injection vector into the agent. Whatever the agent can reach,
an attacker who can write a ticket can try to reach.

## Decision

**An executor port** starts and supervises one run of the lane's runner (ADR-012), usually
`ynh agent run`:

```go
type Executor interface {
    Start(ctx context.Context, spec RunSpec) (Run, error)
}
type Run interface {
    Events() <-chan TrajectoryEvent      // the --emit-jsonl stream
    Control(msg ControlMsg) error        // ynh's stdin NDJSON channel
    Wait() (RunResult, error)            // the --format json object
}
```

| Executor | Runs the runner as | Host |
|---|---|---|
| `process` | a child process | daemon, interactive and shadow mode only |
| `docker` | a container pinned by digest; for ynh, `ynh image <harness> --entrypoint agent` | daemon, hosted |
| `ecs`, `k8s-job` | a remote task; ynf tails its event stream and relays control messages | hosted |
| `ci-inline` | the current CI job | CI-native |

Every run gets its own worktree, step directory and **tool caches**. A cache shared between
worktrees is a correctness problem, not only a containment one. golangci-lint's analysis cache
attributes a finding to whichever copy of identical code it saw first, so a sensor scoped with
`--new-from-merge-base` in a second worktree silently drops it. Go's test cache replays a flaky
test's one pass forever, so the run can never look stuck. Contained executors start each run with
empty caches; the `process` executor points `XDG_CACHE_HOME` and the common tool cache variables
into the step directory. Separately, a sensor that judges a run should not read results from a
cache at all (`go test -count=1`). Worktree paths are resolved to their real path before use: on
macOS the temp directory is under `/var`, a symlink to `/private/var`, and when git and a linter
disagree about a file's path, `--new-from-merge-base` matches nothing and the sensor passes.

With the ynh runner, ynf passes `--emit-jsonl <step dir>/trajectory.jsonl`, so ynh writes a
checkpoint (ynh only checkpoints when given a real emit path).

**The agent never holds forge write credentials.** The worker receives only what the harness's
`env_passthrough` declares: model credentials and, where needed, read-only tokens. When the run
ends, ynf itself reads the worktree (`base_commit` and `changed_files` from the result), commits
with ynf's trailers (ADR-010), pushes, opens or updates the pull request, and comments, using
the GitHub App and JIRA credentials only ynf holds. A prompt-injected agent can at worst produce
a bad diff, which a human reviews.

**A diff gate before every push.** ynf refuses to push, and escalates, when the diff:

- touches paths outside the lane's `allowed_paths` (all paths by default)
- touches sensor configuration, baselines or calibration references
- touches CI workflow files, CODEOWNERS, or ynf's resolved repository folder
  (`.agents/factory/` or a fallback, ADR-009), which holds the lane policy itself

This works with any runner, and with ynh it catches what the `tamper` outcome cannot: changes
outside the files ynh's baseline knows about.

**Containment is mandatory for unattended lanes.** A lane whose trigger is not a human at a
terminal must use `docker`, `ecs`, `k8s-job` or `ci-inline`. `process` is accepted only by
`ynf step --interactive` and `ynf shadow`. ynh's `--sandbox srt` stacks on top where the backend
supports it. A lane that asks for containment the host cannot provide fails to load; it never
degrades to uncontained.

**Egress is the lane's alone.** A harness declares nothing about the network: it does what it
asks for, and is written as if it could reach anything. The lane decides what a run may reach in
this setting:

```yaml
run:
  egress:
    allow:
      - proxy.golang.org
      - sum.golang.org
      - registry.npmjs.org
      - "*.githubusercontent.com"
```

A contained executor denies everything else. The model provider's endpoint is allowed implicitly,
derived from the run's backend rather than declared by the harness (default, 2026-10-03).

Running a harness in a lane is where a mismatch shows up, so it has to show up legibly rather than
as a timeout three tools deep. Egress goes through an allow-list proxy in the executor that logs
every denial. With docker, a lane that allows no hosts runs with `--network none`; one that
allows hosts runs on a per-run `--internal` network, which has no route out, beside a proxy
container (ynf itself, `ynf egress-proxy`) that is the only thing attached to both that network
and the outside. The proxy is reached through the standard `HTTP(S)_PROXY` variables; a tool that
ignores them simply cannot connect. Every job container also drops all capabilities, cannot gain
privileges, and is named, so a run whose lease is lost is removed rather than left running. A run with denials records them in its step log, and each denied host becomes a
failure signature, `sig/egress/denied/<host>` (ADR-008). A lane that keeps hitting the same
denial says exactly which line to add, or which harness behaviour to question.

**No mode opens egress**, shadow mode included. Shadow runs the real agent on real ticket text,
the input most likely to carry an injected instruction, so it gets the lane's policy like any
other run. Its denials are logged the same way, which is how a lane's `allow` list is discovered
safely. Yield measured under open egress would also be the yield of a different system.

**Adopted items get extra guards:** commits are added, never force-pushed; the step aborts and
re-queues if the head moved since it was probed; drafts and fork pull requests are skipped by
default; the pull request author stays the owner of record.

## Alternatives

- **Let the agent push, open pull requests and comment through an MCP server.** Simpler, and the
  common pattern, but it hands write credentials to the component that reads untrusted text.
- **Shadow mode with open but logged egress, to propose an `allow` list.** Rejected: it opens an
  exfiltration path exactly where the input is least vetted.
- **`unsafe_uncontained: true` as a lane escape hatch.** Rejected: ynh's own rule is that a containment control that cannot be applied is an error, not a warning.

## Consequences

- A laptop daemon running unattended lanes needs Docker (or a compatible runtime).
- Harnesses that today rely on the agent running `gh pr create` need a focus that stops at the
  diff when run under ynf.

## Open questions

- None open.

## History

- 2026-10-03: drafted.
