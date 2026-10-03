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

An item is named `owner/name#number`, or by its key, `item/github/<owner>/<name>/issues/<number>`.

## Commands

| Command | Does |
|---|---|
| `ynf version` | Prints the version. |
| `ynf doctor` | Checks the config, the store, each repository's lanes (and any shadowed factory folder), and git, docker, ynh and ynm. ynh and ynm are optional. |
| `ynf lanes validate [--file <path>]` | Validates a lanes file against the schema. Default `.agents/factory/lanes.yaml`. |
| `ynf lanes show [--repo <owner/name>] [<lane>]` | The lanes ynf reads from a repository's default branch, with defaults applied. |
| `ynf sweep [--until-settled] [--timeout 20m] [--interval 15s] [--lane <name>]...` | Runs every lane's searches, then steps every due item. With `--until-settled`, repeats until every item is settled or the timeout passes. |
| `ynf serve [--listen <addr>] [--webhook-secret-env <var>] [--lane <name>]...` | `sweep` every minute, until interrupted. With `--listen`, also receives GitHub webhooks at `POST /webhook/github` (and `GET /healthz`): each is verified against the secret in `--webhook-secret-env` (default `YNF_WEBHOOK_SECRET`; serve refuses to listen without one), de-duplicated by delivery id, and handled in order. A webhook is a hint: facts are probed fresh. |
| `ynf step --github-event <file> --github-event-name <name>` | The CI-native host: handle one GitHub event, as a workflow's trigger gives it (`GITHUB_EVENT_PATH`, `GITHUB_EVENT_NAME`). A `schedule` or `workflow_dispatch` event runs the reconciliation sweep. |
| `ynf items ls` | Every tracked item: lane, state, pull request, reason. |
| `ynf items show <item>` | The item document. |
| `ynf items log <item>` | Every decision, run, action and note, in order. |
| `ynf items retry <item>` | Puts an escalated or quarantined item back to ready, clearing its counters. Refused while another instance holds it. |
| `ynf items release <item>` | Clears the item's lease, for one left by an instance that died. |
| `ynf pause <lane> --reason <text> [--repo <owner/name>]` | Pauses a lane: its tracked items carry on, but nothing new starts. Recorded with who and why. |
| `ynf resume <lane> --reason <text> [--repo <owner/name>]` | Resumes a lane, with a reason, also recorded: stop conditions are changed deliberately (ADR-010). |
| `ynf stats [--lane <name>]...` | Every lane's items, proposals, merged, rejected, yield, whether it is paused and why, and its top failure signatures. |
| `ynf replay <item> [--policy <file>]` | Recomputes every recorded decision, under the recorded lane or the same-named lane in `<file>`, and says which differ. |

## What the log shows

Every decision (`decided`: item, event, state, reason), every run (`run started`: lane, runner, executor, image, attempt; `run in progress` every 30 seconds: elapsed, turns and the latest event from the runner's trajectory; `run finished`: outcome, exit, duration, files changed, hosts denied) and every forge action (`action`: what, ok, detail). A long agent run is never silent.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. |
| `2` | Usage: an unknown command, flag or item name. |
| `20` | An adapter failed: the forge, git, docker or the store. |
| `30` | The config or a lanes file is invalid, or `doctor` found a problem. |
| `31` | `sweep --until-settled` timed out before every item settled. |
| `32` | `replay` found decisions that differ. |

With `--format json`, an error is `{"error": {"code": <exit code>, "message": "..."}}` on stdout.

## Environment

| Variable | Used for |
|---|---|
| `GITHUB_TOKEN` (or `github.token_env`) | The forge and git pushes. Falls back to `gh auth token`. |
| `YNF_CONFIG`, `YNF_FORMAT` | Fallbacks for `--config` and `--format`. |
| `YNF_GITHUB_API` | Another GitHub API base URL (tests, GitHub Enterprise). |
