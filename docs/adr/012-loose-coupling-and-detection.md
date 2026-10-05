# ADR-012: Loose coupling and zero-config detection

Status: draft (2026-10-03)
Satisfies: FR-11, FR-12, FR-19, FR-20, NFR-8, NFR-11

## Context

ynf is designed alongside ynh and ynm, and it would be easy to assume both: lanes naming ynh
harnesses and branching on ynh's exit codes, commits carrying a `YNH-Session` trailer. That would
make ynf useless to anyone running a different agent loop, and make every change to ynh's
contract a change to ynf's policy language.

The opposite failure is just as real: if using ynh and ynm with ynf needs configuration, the
three tools feel like three products. When they are installed, it should just work.

## Decision

**Ports, each with providers.** ynf depends only on its own ports; ynh, ynm, GitHub and JIRA are
providers behind them, never assumptions in the core.

| Port | Providers | What the core sees |
|---|---|---|
| Runner | `ynh` (ynh agent run), `command` (any command) | a `RunSpec` in, a `RunResult` with a ynf **outcome** out |
| Executor (ADR-007) | `process`, `docker`, `inline` | a run started, supervised and stopped |
| Tracker (ADR-003) | `github`, `mcp`, `adhoc` | `get`, `comment`, `label`, and `search` where offered |
| Forge (ADR-003) | `github` | pull requests, checks, reviews, branches and files |
| Memory (ADR-008) | `ynm`, `none` | `Remember(record)`, `Recall(subject)` for people |

**Instances are coupled only by these contracts.** A ynf instance and the ynh instances it runs
are separate programs: ynf knows a run only as a request (an image, a repository at a ref, a task,
budgets, a focus and profile by name) and a result in ynh's own format. A ynh image knows nothing
of ynf: any ynf can run any ynh image that supports what the lane needs, and so can a person or a
plain CI job. ynh's side of the contract is ynh's own published interface, `ynh agent run`, its
result and trajectory schemas and its capabilities version, which ynh versions for exactly this.

**ynf's own outcome vocabulary.** Lane rules branch on outcomes, never on a provider's exit codes:

| Outcome | Meaning | ynh exit codes |
|---|---|---|
| `converged` | the run reached its own definition of done | 0 |
| `budget` | a cap stopped it (`bound_by` says which) | 10, 11, 12, 15 |
| `stuck` | the run detected it was making no progress | 13 |
| `tamper` | the run altered what it is judged against; never retried | 14 |
| `operator_error` | configuration or gate fault; a human must fix something | 22 |
| `error` | the runner failed (worker crash, resume failure) | 20, 21 |
| `aborted` | stopped on purpose (plan rejected, interrupted by ynf) | 30, 31 |

The `ynh` provider owns that mapping, and pins the range of ynh's run result schema it
understands. The `command` provider maps exit code 0 to `converged` and anything else to
`error`, unless the command writes a result file in ynf's `RunResult` schema, in which case
it says exactly what happened.

**The image is the source of truth for its harness.** The ynh provider reads what an image's
harness declares from the image itself, by asking the ynh inside it, cached per image digest:

```
ynh ls --format json                  the harness the image carries, and ynh's capabilities
ynh info <harness id> --format json   its manifest: focuses (prompt, profile), env_passthrough,
                                      agent budgets, sensors
```

ynf resolves a lane's focus to its prompt and profile, checks the harness passes the variables the
lane gives it, and checks the lane only tightens budgets and scopes declared sensors (ADR-006), all
against that answer and never against the repository's working copy, which may differ from what
the image carries or not contain the harness at all. ynf asks the image's ynh rather than the
host's, because the image's runs the agent.

**Capabilities are checked, and a missing ability fails loudly.** Before using an ability, ynf
checks the providing ynh reports a capabilities version that has it (0.9.0 for `--auto-approve`),
and the same holds for any ability ynf asks of another tool. A missing ability refuses the work
before anything runs, naming what is missing and what was found; it never degrades quietly.

**Provider-specific settings live in a provider block,** so the lane's shape does not change
with the runner:

```yaml
run:
  runner: ynh                      # or: command
  image: ghcr.io/eyelock/ynh-lint@sha256:…   # or ynh.harness: <folder>, built when unpublished
  ynh:
    focus: tidy
    budgets: { max_turns: 20 }     # may only tighten (ADR-006)
  # command:
  #   argv: ["./scripts/fix-lint.sh", "{task_file}"]
  #   result_file: "{run_dir}/result.json"
```

**Zero config: detect, then record what was detected.** With no explicit setting, ynf detects:

| Provider | Detected when |
|---|---|
| runner `ynh` | `ynh` is on `PATH` (or `YNF_YNH_BIN`) and `ynh version --format json` is in the supported range |
| memory `ynm` | `ynm` is on `PATH`, and the repository has `.ynm/` or the user has `~/.ynm/`; or a `YNM_URL` endpoint answers |
| tracker and forge `github` for github.com | always available; any other instance, GitHub Enterprise Server or an `mcp` tracker, is configured, never detected |

Precedence is explicit config, then detection, then the built-in fallback (`command` for the
runner, `none` for memory). A lane that names a provider explicitly and cannot get it is refused
before anything runs, and never falls back. A lane that relies on detection runs with whatever was
detected: with no runner named, a lane with a `ynh` block runs as ynh when ynh is detected and
otherwise as its `command` block, and a lane with neither is refused. Every step records the
providers it used and their versions, so a run on one machine is explainable on another, and
`ynf doctor` prints which providers are installed and their versions.

**ynh-only features stay in the ynh provider.** Budget tightening, sensor overlays, the control
channel, checkpoint paths, and the `YNH-Session` trailer exist only when the runner is `ynh`.
Everything ynf itself guarantees (claims, containment, the credential split, the diff gate,
the `YNF-Item`, `YNF-Step` and `YNF-Run` trailers) works with any runner.

## Alternatives

- **ynh as a hard dependency.** Simplest. Rejected: ynf's value
  is the outer loop, which does not care what the inner loop is.
- **Detection only, no explicit config.** Rejected: an unattended lane must not change behaviour
  because someone installed a binary on the host.

## Consequences

- The `command` provider makes ynf usable with a plain `claude -p` script, another harness tool,
  or a deterministic codemod with no model at all, which is also the cheapest way to test ynf.
- Adding a runner is a provider plus its outcome mapping; the decider and lanes do not change.

## Open questions

- Should `ynf init` offer to install ynm's ynh plugin when it detects both, or only suggest it?

## History

- 2026-10-03: drafted.
