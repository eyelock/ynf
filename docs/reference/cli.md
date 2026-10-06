# CLI

```
ynf [global flags] <command> [flags]
```

## Global flags

| Flag | Default | Meaning |
|---|---|---|
| `--config <path>` | `config.yaml` in `~/.agents/factory/`, `~/.ynh/ynf/`, `~/.ynm/ynf/` or `~/.ynf/` | The config file. `YNF_CONFIG` is the fallback. |
| `--format text\|json` | `text` | `json` prints one object per command. `YNF_FORMAT` is the fallback. |
| `--interactive` | off | Allows the uncontained `process` executor (ADR-007). |
| `--log-file <path>` | none | Also write the log to this file, as it happens. `YNF_LOG_FILE` is the fallback. |
| `--log-format text\|json` | `text` | `text` is logfmt for people; `json` is one object per line for tools. `YNF_LOG_FORMAT` is the fallback. |
| `-v` | off | Debug logging to stderr. |

An item is named by its reference: `host/owner/name#number` for a GitHub issue or an adopted pull
request (`github.com/eyelock/ynh#77`), or `owner/name#number` for one on the configured forge. GitHub
numbers issues and pull requests from one sequence per repository, so the number names whichever
item exists. The `items` commands and `replay` find it either way. Commands that read a ticket
(`start`, `ticket`, `shadow run --ticket`) take the number as an issue. An item can also be named
by its key, `item/<host>/<owner>/<name>/issues/<number>`, or `…/pulls/<number>` for an adopted pull
request. The host is the forge's own, so an issue on GitHub Enterprise Server is
`item/github.acme.internal/…`.

## Commands

| Command | Does |
|---|---|
| `ynf version` | Prints the version. |
| `ynf doctor` | Checks the config, the store, each repository's lanes (and any shadowed factory folder), each forge and tracker, and git, docker, ynh and ynm. ynh and ynm are optional. When memory writes are waiting for ynm it adds a `memory queue` line, `N memory writes queued since <time>`, as a warning, not a failure; nothing is said when the queue is empty. A `required checks <repository>` line for each enrolled repository says which checks its default branch requires, from branch protection and rulesets together; when neither can be read it says so, as a warning, not a failure, because every check then gates a pull request. The ynh line is the detection a lane with no `run.runner` uses. A `ynr` line, also optional, shows what `ynr info --format json` answered, and says plainly when `telemetry.collector` is on and `ynr` is not there: a factory job then runs on without a collector. With `telemetry.spool` set, a `telemetry` line says the root is usable. |
| `ynf forges` | Each forge ynf works with, checked by reaching an enrolled repository on it. |
| `ynf trackers` | Each tracker: every forge's issues, and each declared tracker, whose MCP server is started and checked for the tools it is configured to call. |
| `ynf ticket <ref>` | Reads a ticket exactly as `start` would, without starting anything: its title, state, labels, the repository it names, and its body. For checking a tracker's `fields`. |
| `ynf harness [<repo>]...` | How each lane of each repository (default: every enrolled one) runs: its runner, executor and harness. A lane with no `run.runner` shows what it resolves to on this host, such as `ynh (detected 0.10.0)` or `command (ynh not found)`. A harness in a published image, or installed beside an inline ynf, is read and checked against its lane: budgets only tightened, sensors only scoped. Exits 30 if one can't be read or doesn't fit. |
| `ynf lanes validate [--file <path>] [--repo <owner/name>]` | Validates a lanes file against the schema. Default `.agents/factory/lanes.yaml`. With `--repo` the file is a repository's own layer: it is laid over the configuration repository's lanes as ynf does and the merged result is validated, so a layer that is only overrides can be checked before it is pushed. Says which configuration commit it was merged over. With no configuration repository the file is the whole policy. |
| `ynf lanes show [--repo <owner/name>] [<lane>]` | The lanes ynf reads from a repository's default branch, with defaults applied, as YAML (`--format json` for JSON), and the source of each value when there is a configuration repository. Under `resolves`, what each lane with no `run.runner` runs as on this host. |
| `ynf start <ref> [--repo <host/owner/name>] [--lane <name>] [--auto-approve edits\|all] [--detach]` | An instruction: take on this one ticket now (ADR-003). It reads the ticket, checks its repository is enrolled and reachable, that a lane takes it, and that the ticket has every label the lane's runs read (a `{label.<prefix>}` in its command or sensor scopes) (`--lane`, or the repository's only originate lane), and refuses with exit 33 before creating anything if any check fails. It then steps the item until it waits on something outside ynf (CI, a review, a person) and prints its state and pull request. Run from a terminal it is attended: the uncontained `process` executor is allowed, and only `--auto-approve` here switches off the agent's approval prompts on this machine (a lane's `auto_approve` applies only inside containment, ADR-007). `--detach` only records it, for a running `ynf serve` to step. A GitHub issue's code goes to its own repository; give `--repo` for any other ticket. |
| `ynf start --prompt <text> [--label <prefix:value>]... --repo <owner/name> [--lane <name>] [--auto-approve edits\|all] [--detach]` | The same, for work with no ticket: the prompt is the task, the branch is `ynf/adhoc-<id>`, and the draft pull request carries the conversation. `--label` gives the work the labels a ticket would have, for a lane whose runs read them (`{label.pkg}`). |
| `ynf sweep [--until-settled] [--timeout 20m] [--interval 15s] [--lane <name>]...` | Runs every lane's searches, then steps every due item. With `--until-settled`, repeats until every item is settled or the timeout passes. |
| `ynf serve [--listen <addr>] [--webhook-secret-env <var>] [--start-token-env <var>] [--lane <name>]...` | `sweep` every minute, until interrupted. With `--listen`, also receives GitHub webhooks at `POST /webhook/github` (and `GET /healthz`): each is verified against the secret in `--webhook-secret-env` (default `YNF_WEBHOOK_SECRET`; serve refuses to listen without one), de-duplicated by delivery id, and handled in order. A webhook is a hint: facts are probed fresh. When the variable named by `--start-token-env` (default `YNF_START_TOKEN`) is set, `POST /start` takes the same instruction as `ynf start --detach`, as JSON (`{"ref": …}` or `{"prompt": …, "repo": …, "labels": […]}`, with an optional `"lane"`), from callers presenting that token as a bearer token: `202` with the item, `422` when refused. Without the variable the endpoint does not exist. |
| `ynf handle --github-event <file> --github-event-name <name> [--lane <name>]...` | The CI-native host: handle one GitHub event, as a workflow's trigger gives it (`GITHUB_EVENT_PATH`, `GITHUB_EVENT_NAME`). A `schedule` or `workflow_dispatch` event runs the reconciliation sweep. `--lane`, repeatable, limits it as it does `sweep`: the sweep and its due items act on those lanes only, and an issue or pull request event steps only its items in those lanes. A workflow can run one lane on its own schedule. |
| `ynf items ls` | Every tracked item: lane, state, pull request, reason. |
| `ynf items show <item>` | The item document. Its `reason` is the latest decision's, including one that kept the item where it was, such as `proposed` with "CI pending". A settled item keeps the reason it was settled for. |
| `ynf items log <item>` | Every decision, run, action and note, in order. |
| `ynf items retry <item>` | Puts an escalated or quarantined item back to ready, clearing its counters. Refused for an item in any other state, and while another instance holds it. |
| `ynf items release <item>` | Clears the item's lease, for one left by an instance that died, and makes the item due now so its work restarts at once. A settled item is only unleased. |
| `ynf pause <lane> --reason <text> [--repo <owner/name>]` | Pauses a lane: its tracked items carry on, but nothing new starts. Recorded with who and why. |
| `ynf resume <lane> --reason <text> [--repo <owner/name>]` | Resumes a lane, with a reason, also recorded: stop conditions are changed deliberately (ADR-010). |
| `ynf stats [--lane <name>]...` | Every lane's items, proposals, merged, rejected, yield, whether it is paused and why, its top failure signatures, and its runs by model and effort: runs, converged, turns per run (a ynh turn is one plan-and-check iteration, not one model call), input and output tokens and cache-read tokens per run, cost where reported, and the proposals, merges and rejections of each model's changes. A run on the vendor's default model is listed as `<backend> (model not reported)`, and a command lane as `none (command)`. |
| `ynf shadow run <lane> [--repo <owner/name>]... [--since 90d] [--limit 20] [--ticket <ref>]...` | Shadow mode (FR-26): runs a lane against closed tickets whose answer is known, and proposes nothing. The candidates are closed tickets the lane would have taken, closed within `--since`, that a merged pull request fixed: the lane's `github.search` intakes are read as closed (`is:open` becomes `is:closed`, with a `closed:>=` date), or `--ticket` names tickets directly. Each runs exactly as the factory would run it (the lane's runner, harness, focus, budgets, model, effort, executor and containment, and the task built from the ticket as it was) on the commit before the human fix, which is the first parent of the fix's merge commit. The lane, its policy, and the agent image or harness folder are resolved once and used for every candidate, and recorded. Nothing outward happens: no push, pull request, ticket comment or label, memory write, item or `stats` count. `--limit` is per repository. The process executor is allowed, as for an attended `start`. A disabled lane can be shadowed, which is the point. A lane that adopts pull requests, or whose intake is not a `github.search`, is refused with exit 2. It says how many tickets it skipped and why, and stops early if a run ends `operator_error`, since every candidate would end the same way. |
| `ynf shadow ls` | Every shadow run: id, lane, when, candidates, attempted and graded. |
| `ynf shadow grade [<shadow run id>] [--attempt <id> --a <grade> --b <grade>] [--regrade]` | Blind grading by a person at a terminal, for the run named or the latest. For each attempt it shows the ticket, then two patches labelled A and B in a random order that is recorded but not shown (the agent's and the human's), and asks for a grade for each: `equivalent`, `different-valid`, `superficial`, `wrong` or `does-not-build`. Only then does it say which was which and how the run ended. Grading both is deliberate: the human patch checks the grader, and each grade is a labelled example. An attempt with an empty agent patch is graded `wrong` automatically, with a note, and is never shown. For scripts and tests, `--attempt <id> --a <grade> --b <grade>` gives both grades for patches A and B in the attempt's recorded order. A grade is kept; `--regrade` replaces it. |
| `ynf shadow report [<shadow run id> \| --lane <lane>]` | The yield `y` (equivalent plus different-valid, over graded agent attempts) with its Wilson 95% interval, per repository and pooled; until attempts are graded, the automatic upper bound (converged and the diff gate would have accepted it, over attempted), labelled as an upper bound that is not graded. Also the outcomes, the superficial count, total and per-attempt cost, the pins (lane, policy hash, harness and its commit, image, ynh version, model, effort, and anything that differed between attempts), and the grades of the human patches as a check on the grader. Lanes do not yet declare the human time `h`, so `y` is not compared with `y* = r / h`. `--format json` gives all of it. |
| `ynf telemetry registry [--format json]` | Prints the OpenTelemetry names ynf emits under the `ynf.` prefix, and the standard ones it uses, as the registry embedded in the binary (an OpenTelemetry Weaver registry, in `telemetry/registry`). `json` gives the whole registry: its tool name and version, the pinned semantic-conventions release, every attribute, span, event and metric, and the cardinality limit of each metric attribute. `--format` may come before or after `telemetry`. |
| `ynf replay <item> [--policy <file>]` | Recomputes every recorded decision, under the recorded lane or the same-named lane in `<file>`, and says which differ. A `<file>` that is invalid alone is taken as the item's repository's own layer and merged over the configuration repository's lanes, as `lanes validate --repo` does. |

## What the log shows

Every decision (`decided`: item, event, state, reason), every run (`run started`: lane, runner, executor, image, attempt; `run in progress` every 30 seconds: elapsed, turns and the latest event from the runner's trajectory; `run finished`: outcome, exit, duration, files changed, hosts denied) and every forge action (`action`: what, ok, detail). A long agent run is never silent.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. |
| `2` | Usage: an unknown command, flag or item name, `items retry` on an item that is not escalated or quarantined, or a `shadow` command that was refused or named a shadow run, attempt or lane that does not exist. |
| `20` | An adapter failed: the forge, git, docker or the store. |
| `30` | The config or a lanes file is invalid, or `doctor` found a problem. |
| `31` | `sweep --until-settled` timed out before every item settled. |
| `32` | `replay` found decisions that differ. |
| `33` | `start` refused the item before creating anything: the reference does not resolve or cannot be read, the repository is not enrolled or reachable, or no lane takes it. |

With `--format json`, an error is `{"error": {"code": <exit code>, "message": "..."}}` on stdout.

## Environment

ynf writes OpenTelemetry where [See ynf in OpenTelemetry](../how-to/see-ynf-in-opentelemetry.md) says; the variables it reads for that are `OTEL_EXPORTER_OTLP_*`, `YNR_SPOOL`, `XDG_STATE_HOME`, `OTEL_RESOURCE_ATTRIBUTES`, `TRACEPARENT` and `TRACESTATE`, listed there. The `ynr` that `doctor` asks and a factory job with `telemetry.collector` on starts is the one on `PATH`; ynf finds ynh and ynm the same way, and nowhere else. A factory job is `sweep`, `serve`, `handle` and `shadow run`; [Configuration](configuration.md#a-factory-job-with-the-collector-on) says what it does when the collector is on.

| Variable | Used for |
|---|---|
| `GITHUB_TOKEN` (or `github.token_env`) | The forge and git pushes. Falls back to `gh auth token`. |
| `YNF_CONFIG`, `YNF_FORMAT` | Fallbacks for `--config` and `--format`. |
| `YNF_GITHUB_API` | Another GitHub API base URL (tests, GitHub Enterprise). |
| `YNF_WEBHOOK_SECRET` | The secret `serve --listen` verifies GitHub webhooks against (`--webhook-secret-env` names another variable). |
| `YNF_START_TOKEN` | The bearer token `POST /start` requires (`--start-token-env` names another variable). Unset, the endpoint does not exist. |
