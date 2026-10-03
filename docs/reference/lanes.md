# Lanes

`.agents/factory/lanes.yaml` on a repository's default branch, validated against
[`docs/schema/lanes.schema.json`](../schema/lanes.schema.json). The design is
[ADR-006](../adr/006-decider-and-lane-policy.md); this page says what each key does and what is
carried out today.

## A lane

| Key | Meaning | Today |
|---|---|---|
| `kind` | `originate` (ynf opens the branch and pull request) or `adopt` (someone else's pull request) | `originate`; `adopt` lanes are read and skipped |
| `enabled` | `false` switches the lane off: its items are tracked and ignored | yes |
| `intake` | Where work comes from | `github.search`; `jira.search`, webhooks and topics are read and skipped |
| `guards.eligible` | A CEL expression over structured facts; false means ignored | yes |
| `run.runner` | `command` or `ynh` | `command`; `ynh` builds its command but needs an image with ynh in it |
| `run.executor` | `docker`, `process`, `ecs`, `k8s-job`, `ci-inline` | `docker`; `process` with `--interactive` |
| `run.image` | The container image a command runs in | yes |
| `run.egress.allow` | Hosts a run may reach | an empty list (no network); a non-empty list waits for the egress proxy and fails the run as `operator_error` |
| `run.command.argv` | The command, run without a shell; `{label.<prefix>}`, `{task_file}`, `{run_dir}` are filled in | yes |
| `run.command.result_file` | A file the command writes with `{outcome, detail, model, session}` | yes |
| `run.ynh` | `harness`, `focus`, `profile`, `sandbox`, `budgets`, `sensor_scope` | built into the command; see `run.runner` |
| `when` | Reactions to `converged`, `ci_failed`, `changes_requested` and `outcome.<name>` | `open_pr`, `escalate`, `quarantine`, `comment`, `close`, `retry`/`then`, `resume_with`/`max`; `push_commit` and `request_review` escalate |
| `pr.allowed_paths` | The diff gate refuses changes outside these | yes |
| `pr.protected_paths` | Refused as well as the built-in protected paths | yes |
| `pr.draft` | Open pull requests as drafts | yes (default `true`) |
| `stop` | Stop conditions (ADR-010) | not yet |
| `attempts` | Runs that never finish before the item is quarantined | yes (default 3) |

## The built-in protected paths

Refused in every lane, whatever `allowed_paths` says: `.github/workflows/**`, `.github/actions/**`,
`**/CODEOWNERS`, `.agents/**`, `.ynh/**`, `.ynm/**`, `.ynf/**`.

## Outcomes

A run ends in one of ynf's outcomes (ADR-012), which `when` reacts to:
`converged`, `budget`, `stuck`, `tamper`, `operator_error`, `error`, `aborted`. A `converged` run
that changed nothing is escalated. Anything without a reaction in `when` is escalated.
