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
| `run.runner` | `command` or `ynh`. Omit it and ynf chooses (ADR-012): `ynh` when the lane has a `ynh` block and ynh is detected on the host, else the lane's `command` block; with neither, the lane is refused with `operator_error` before anything runs. A lane that omits it needs a `ynh` block, a `command` block or both | both. ynh is detected when `ynh version --format json` answers on `PATH` with capabilities 0.9.0 or later, checked once per process, on the host even when the run is in a published image. A lane that names `ynh` never falls back: with no usable ynh where it runs on the host (an image ynf builds, `process`, `inline`) it is refused. The run record says `runner_detected` and the ynh version; `ynf harness` and `ynf lanes show` say what an unnamed runner resolves to here. On docker, a `ynh` lane runs in an agent image ynf builds from the harness with `ynh image --entrypoint agent` (ynh on PATH), or `run.image` |
| `run.executor` | `docker`, `process`, `ecs`, `k8s-job`, `ci-inline` | `docker`; `process` with `--interactive` |
| `run.image` | The container image a run uses: the command's, or a published agent image for ynh | yes |
| `run.command.image` | The image the command runs in; when the lane resolves to the command runner it wins over `run.image`, and when it resolves to ynh it is ignored, so a lane with both blocks can name each one's image | yes |
| `run.egress.allow` | Hosts a run may reach | yes: an empty list is no network at all; otherwise an internal network whose only way out is ynf's allow-list proxy. Each denied host is recorded on the run and counted as `sig/egress/denied/<host>` |
| `run.command.argv` | The command, run without a shell; `{label.<prefix>}`, `{task_file}`, `{run_dir}` are filled in | yes |
| `run.command.image_user` | `true` keeps the image's own user and home on a docker executor, as a ynh-built agent image does, instead of running as ynf's user. ynf finds that user's id from the image and names it in the run's spool manifest, so `ynr serve` reads what the run writes there. Default `false` | yes |
| `run.command.result_file` | A file the command writes with `{outcome, detail, model, session}` | yes |
| `run.env` | Variables passed into the run by name, such as the model key; values never logged | yes |
| `run.ynh` | `harness`, `vendor`, `base`, `focus`, `profile`, `sandbox`, `model`, `effort` (`low`, `medium` or `high`; needs ynh 0.10.0 or later), `budgets`, `sensor_scope` (each sensor's declared command narrowed: a path word replaced by one beneath it, or paths appended; anything else is refused before the run), `telemetry_relay`. `harness` is one of three things. The id of an installed harness. A folder in the checkout, or an absolute one (`.` is the harness the repository carries): on docker it is built into an agent image; on `process`, and on `inline` when no harness is installed in the image, it is installed into a ynh home of the run's own (`YNH_HOME` in the run's folder, so yours is never read or written) and run by the id ynh gives it. A folder that is there and cannot be read, such as a malformed `plugin.json`, ends the run as `operator_error` with the error, and a readable one is always checked against the lane. Or `<repository>@<tag-or-commit>` (`github.com/org/harness@v1.2.0`, `git@github.com:org/harness@3f9c2e1`, `file:///srv/harness.git@v1`), a harness pinned from git, on `process` and `inline` only. ynf has ynh install exactly that (`ynh install <repository> --ref <tag-or-commit>`) into the run's own home, reads the manifest, focus and budgets from what ynh installed, checks the lane against them, runs it by its id, and records the harness, version and commit in the run record (`harness`, `harness_sha`, `harness_pin`); nothing is copied into the target repository. A branch is refused, because it moves. The install fetches the repository, so it happens on the host before the run starts and the run's egress is never asked for it; under `inline` the job's own network policy applies to it. On docker a pinned harness is refused with `operator_error`: use a folder in the repository, or `run.image`. A local repository needs a `file://` URL, because ynh refuses `--ref` for a path. `ynf lanes validate` and `ynf lanes show` list each lane's pin, and `ynf harness` installs it into a temporary home and says what it resolves to | yes; the vendor's API host is allowed through the egress proxy without being listed |
| `run.ynh.telemetry_relay` | `true` sets `YNH_TELEMETRY_RELAY=1` for the lane's runs, so `ynh agent run` starts `ynr relay` beside the vendor CLI, which exports only over the network, and the relay writes what it receives into the run's own spool folder (ynr ADR-004). It needs `telemetry.spool` in ynf's configuration (ynf logs a warning when it is on and there is none), and `ynr` in the run's image or on its `PATH`. A lane that names `runner: command` refuses it at load; a lane with both blocks keeps it for when ynh runs. Default `false` | yes |
| `when` | Reactions to `converged`, `ci_failed`, `changes_requested` and `outcome.<name>` | `open_pr` (originate), `push_commit` (adopt), `escalate`, `quarantine`, `comment`, `close`, `retry`/`then`, `resume_with`/`max`; `request_review` escalates |
| `pr.allowed_paths` | The diff gate refuses changes outside these | yes |
| `pr.protected_paths` | Refused as well as the built-in protected paths | yes |
| `pr.draft` | Open pull requests as drafts | yes (default `true`) |
| `stop` | Stop conditions (ADR-010) | `max_open_proposals` holds new work while that many proposals await review; `yield_floor` pauses the lane once `min_sample` (default 20) proposals are decided below it; `review_time_ceiling` and `escaped_defects` not yet |
| `labels` | What ynf writes on the ticket as the item moves (ADR-003) | `on_claim` (taken on), `on_propose` (pull request open), `on_review` (CI green), `on_escalate` (escalated or quarantined) and `on_done` (merged or closed), each `{add: [...], remove: [...]}`, written through the ticket's tracker as the item enters the state. Remove the triggering label in `on_claim` so nothing finds the ticket twice. A failed write is logged and recorded, never fatal. Each reaction falls back to `defaults.labels` on its own |
| `attempts` | Runs that never finish before the item is quarantined | yes (default 3) |
| `id` | The lane's own id in telemetry, which stays fixed if the repository that defines it moves. Without one, a lane's id is where it is defined, host first, then its name: `github.com/example-org/factory-config#lint-paydown` for a lane in the configuration repository (wherever a repository overrides it), `github.com/eyelock/ynh#docs-refresh` for one the repository defines itself (ADR-006). Letters, digits and `. _ ~ : @ / # -` | yes |

## What guards see of a pull request

`guards.eligible` reads `facts.pr`, read fresh before every decision. Its fields:

| Field | Meaning |
|---|---|
| `number`, `state`, `merged`, `draft`, `fork` | The pull request's own state |
| `changes_requested`, `approved` | From the latest review by each reviewer |
| `ci` | `pending`, `success` or `failure`, summed over the checks that gate (below) |
| `required_unknown` | `true` when neither branch protection nor the repository's rulesets could be read, so no check is marked required |
| `checks` | Every check run and commit status on the head commit, and every required check that has not reported |

Each entry of `checks` has:

| Field | Meaning |
|---|---|
| `name` | The check run's name, or the commit status's context |
| `status` | `expected`, `queued`, `in_progress` or `completed`. A commit status is `completed` unless it is `pending`, which is `in_progress` |
| `conclusion` | `success`, `failure`, `neutral`, `cancelled`, `skipped`, `timed_out`, `action_required`, or empty until the check concludes. A commit status of `error` is `failure` |
| `required` | `true` when the base branch requires it |
| `app`, `app_id` | The GitHub App that reported a check run: its slug and id. Both are empty for a commit status, which has no App |

**Required checks** are the union of the base branch's classic branch protection and the
repository rulesets that apply to it (rules of type `required_status_checks`). A required check
bound to a GitHub App is marked `required` only on a run from that App: a check of the same name
from another App is listed, and is not required. A commit status never meets a requirement bound to
an App.

**A required check that has not started** appears in `checks` with `status: "expected"`, an empty
`conclusion` and `required: true`. If the requirement is bound to an App, `app_id` is that App's id
and `app` is empty. ynf does not say how long a check has been expected: GitHub does not record
when a check was due, and the head commit's date is not when it was pushed. A guard can see that a
check has not started, not for how long.

**`ci`** gates on the required checks when any are marked, and on every check otherwise. It is
`pending` until every gating check has concluded, which includes an `expected` one, and `failure`
as soon as one has failed. An item whose pull request is `pending` stays proposed, and does not
move to `in_review`.

When neither source can be read (a token without the right to read branch protection, and a
repository whose rulesets are unavailable), `required_unknown` is `true`, nothing is marked
required, and every check gates, as it did before ynf read rulesets. ynf logs it once for each
repository for the life of the process, and `ynf doctor` reports it on a `required checks
<repository>` line for each enrolled repository. Give the token read access to the repository's
administration, or to its rulesets, to clear it.

Checks, check runs and statuses are read to the last page.

## The built-in protected paths

Refused in every lane, whatever `allowed_paths` says: `.github/workflows/**`, `.github/actions/**`,
`**/CODEOWNERS`, `.agents/**`, `.ynh/**`, `.ynm/**`, `.ynf/**`.

## Outcomes

A run ends in one of ynf's outcomes (ADR-012), which `when` reacts to:
`converged`, `budget`, `stuck`, `tamper`, `operator_error`, `error`, `aborted`. A `converged` run
that changed nothing is escalated. Anything without a reaction in `when` is escalated.
