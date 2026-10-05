# Lanes

`.agents/factory/lanes.yaml` on a repository's default branch, validated against
[`docs/schema/lanes.schema.json`](../schema/lanes.schema.json). The design is
[ADR-006](../adr/006-decider-and-lane-policy.md); this page says what each key does and what is
carried out today.

## A lane

| Key | Meaning | Today |
|---|---|---|
| `kind` | `originate` (ynf opens the branch and pull request) or `adopt` (someone else's pull request) | both. An adopted pull request is watched until its guard holds, never adopted from a fork or while a draft, and gets ynf's commit pushed on top, never forced; if the author pushes while ynf works, ynf starts again from their new head |
| `enabled` | `false` switches the lane off: its items are tracked and ignored | yes |
| `intake` | Where work comes from | `github.search`, run by every sweep; GitHub webhooks (`ynf serve --listen`, `ynf handle`) step tracked items at once and sweep for new ones; `jira.search` and topics are read and skipped |
| `guards.eligible` | A CEL expression over structured facts; false means ignored | yes |
| `run.runner` | `command` or `ynh`. Omit it and ynf chooses (ADR-012): `ynh` when the lane has a `ynh` block and ynh is detected on the host, else the lane's `command` block; with neither, the lane is refused with `operator_error` before anything runs. A lane that omits it needs a `ynh` block, a `command` block or both | both. ynh is detected when `ynh version --format json` answers on `PATH` (or at `YNF_YNH_BIN`) with capabilities 0.9.0 or later, checked once per process, on the host even when the run is in a published image. A lane that names `ynh` never falls back: with no usable ynh where it runs on the host (an image ynf builds, `process`, `inline`) it is refused. The run record says `runner_detected` and the ynh version; `ynf harness` and `ynf lanes show` say what an unnamed runner resolves to here. On docker, a `ynh` lane runs in an agent image ynf builds from the harness with `ynh image --entrypoint agent` (ynh on PATH), or `run.image` |
| `run.executor` | `docker`, `process`, `ecs`, `k8s-job`, `ci-inline` | `docker`; `process` with `--interactive` |
| `run.image` | The container image a command runs in | yes |
| `run.egress.allow` | Hosts a run may reach | yes: an empty list is no network at all; otherwise an internal network whose only way out is ynf's allow-list proxy. Each denied host is recorded on the run and counted as `sig/egress/denied/<host>` |
| `run.command.argv` | The command, run without a shell; `{label.<prefix>}`, `{task_file}`, `{run_dir}` are filled in | yes |
| `run.command.result_file` | A file the command writes with `{outcome, detail, model, session}` | yes |
| `run.env` | Variables passed into the run by name, such as the model key; values never logged | yes |
| `run.ynh` | `harness`, `vendor`, `base`, `focus`, `profile`, `sandbox`, `model`, `effort` (`low`, `medium` or `high`; needs ynh 0.10.0 or later), `budgets`, `sensor_scope` | yes; the vendor's API host is allowed through the egress proxy without being listed |
| `when` | Reactions to `converged`, `ci_failed`, `changes_requested` and `outcome.<name>` | `open_pr` (originate), `push_commit` (adopt), `escalate`, `quarantine`, `comment`, `close`, `retry`/`then`, `resume_with`/`max`; `request_review` escalates |
| `pr.allowed_paths` | The diff gate refuses changes outside these | yes |
| `pr.protected_paths` | Refused as well as the built-in protected paths | yes |
| `pr.draft` | Open pull requests as drafts | yes (default `true`) |
| `stop` | Stop conditions (ADR-010) | `max_open_proposals` holds new work while that many proposals await review; `yield_floor` pauses the lane once `min_sample` (default 20) proposals are decided below it; `review_time_ceiling` and `escaped_defects` not yet |
| `labels` | What ynf writes on the ticket as the item moves (ADR-003) | `on_claim` (taken on), `on_propose` (pull request open), `on_review` (CI green), `on_escalate` (escalated or quarantined) and `on_done` (merged or closed), each `{add: [...], remove: [...]}`, written through the ticket's tracker as the item enters the state. Remove the triggering label in `on_claim` so nothing finds the ticket twice. A failed write is logged and recorded, never fatal. Each reaction falls back to `defaults.labels` on its own |
| `attempts` | Runs that never finish before the item is quarantined | yes (default 3) |

## The built-in protected paths

Refused in every lane, whatever `allowed_paths` says: `.github/workflows/**`, `.github/actions/**`,
`**/CODEOWNERS`, `.agents/**`, `.ynh/**`, `.ynm/**`, `.ynf/**`.

## Outcomes

A run ends in one of ynf's outcomes (ADR-012), which `when` reacts to:
`converged`, `budget`, `stuck`, `tamper`, `operator_error`, `error`, `aborted`. A `converged` run
that changed nothing is escalated. Anything without a reaction in `when` is escalated.
