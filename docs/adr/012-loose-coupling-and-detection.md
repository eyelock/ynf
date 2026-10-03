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

**Two ports, each with providers.** ynf depends only on its own ports; ynh and ynm are providers
behind them, never assumptions in the core.

| Port | Providers | What the core sees |
|---|---|---|
| Runner | `ynh` (ynh agent run), `command` (any command) | a `RunSpec` in, a `RunResult` with a ynf **outcome** out |
| Memory | `ynm`, `none` | `Remember(record)`, `Context(subject, budget)` |

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

**Provider-specific settings live in a provider block,** so the lane's shape does not change
with the runner:

```yaml
run:
  runner: ynh                      # or: command
  ynh:
    harness: eyelock/ynh-lint@1.4
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

Precedence is explicit config, then detection, then the built-in fallback (`command` for the
runner, `none` for memory). A lane that names a provider explicitly and cannot get it fails to
load. A lane that relies on detection runs with whatever was detected. Every step records the
providers it used and their versions, so a run on one machine is explainable on another, and
`ynf doctor` prints what it detected and why.

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
