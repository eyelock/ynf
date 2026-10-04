# 7. Tickets from elsewhere

Not every piece of work starts as a GitHub issue. This lesson takes on a ticket from a tracker that
isn't GitHub, the sandbox's stand-in for JIRA, and then work with no ticket at all: a prompt.

You need the terminal from lesson 1, with `YNF_CONFIG`, `PATH` and `YNF_SANDBOX_TRACKER_DATA` set.

## The tracker

The configuration repository declares the tracker, in `.agents/factory/factory.yaml`:

```bash
gh api repos/<you>/ynf-sandbox-factory/contents/.agents/factory/factory.yaml \
  -H 'Accept: application/vnd.github.raw'
```

Expected, after its comments:

```yaml
version: 1
repos:
  - github.com/<you>/ynf-sandbox

trackers:
  tracker:
    provider: mcp
    site: https://tracker.ynf-sandbox.invalid
    server:
      command: [ynf-sandbox-tracker]
      env: [YNF_SANDBOX_TRACKER_DATA]
    get: { tool: get_ticket, args: { key: "{key}" } }
    comment: { tool: add_comment, args: { key: "{key}", body: "{text}" } }
    label: { tool: set_labels, args: { key: "{key}", labels: "{labels}" } }
    fields:
      title: result.title
      body: result.body
      labels: result.labels
      status: result.status
      repo: result.repo
    closed_when: 'status == "done"'
```

Read it top to bottom:

- **`provider: mcp`:** ynf reaches the tracker through its MCP server, calling the server's tools
  itself. No model is involved: reading a ticket is a plain tool call.
- **`site`:** the tracker's address. Its host, `tracker.ynf-sandbox.invalid`, is part of every
  ticket's item key, as `github.com` is for issues.
- **`server`:** how to start the server: a command, started with only `PATH`, `HOME` and the
  variables listed in `env`. A hosted server is given by `url` instead.
- **`get`, `comment`, `label`:** which tool to call for each, and with what arguments.
- **`fields`:** CEL expressions that read the `get` tool's result into what ynf needs: a title, a
  body, labels, a status, and the repository the ticket says its code goes to.
- **`closed_when`:** when a ticket counts as closed.

Trackers are declared only here, in the configuration repository, never in a repository ynf works
on. They carry credentials, and they decide which tickets reach which code.

## Read the ticket

```bash
ynf ticket tracker/SBX-1
```

Expected:

```text
tracker.ynf-sandbox.invalid/SBX-1  internal/format is not gofmt-clean, from the tracker
state   open
labels  pkg:internal/format
repo    github.com/<you>/ynf-sandbox

`internal/format` is not `gofmt`-clean.
```

That's what `fields` produced from the tool's result. When you connect a real tracker, this is the
command you run until every line is right, before any work depends on it.

## Start it

A tracker ticket doesn't belong to a repository the way a GitHub issue does, so ynf needs to be
told where its code goes:

```bash
ynf start tracker/SBX-1 --lane gofmt
echo "exit $?"
```

Expected:

```text
ynf: tracker.ynf-sandbox.invalid/SBX-1: say which repository its code goes to, with --repo
exit 33
```

Exit 33 means ynf refused before creating anything. Now say where:

```bash
ynf start tracker/SBX-1 --repo <you>/ynf-sandbox --lane gofmt
```

Expected, after the log:

```text
tracker.ynf-sandbox.invalid/SBX-1: proposed in lane gofmt (proposed as #16)
pull request: https://github.com/<you>/ynf-sandbox/pull/16
```

The ticket names its repository in a structured field, `repo`, and the two have to agree. That
catches a wrong ticket or a typo before anything runs. To see it, change `repo` in
`~/ynf-tutorial/tickets.json` to `github.com/<you>/elsewhere` and start it again: ynf refuses, with
`tracker.ynf-sandbox.invalid/SBX-1 says its code goes to github.com/<you>/elsewhere, not
<you>/ynf-sandbox`. Change it back afterwards. Only a structured field counts: ynf never guesses a repository from a ticket's
text.

## What happened on the ticket

```bash
cat ~/ynf-tutorial/tickets.json
```

Expected, among the output:

```json
    "labels": [
      "pkg:internal/format",
      "ynf:proposed"
    ],
    "status": "open",
    "repo": "github.com/<you>/ynf-sandbox",
    "comments": [
      "**ynf** proposed https://github.com/<you>/ynf-sandbox/pull/16.\n\n<!-- ynf:open_pr=01M43K2W7H4D9X0QF6B3N8R5TE -->"
    ]
```

ynf labelled the ticket and commented on it through the tracker's tools, as it did on the GitHub
issue in lesson 3. The comment links the pull request by its full address, since the tracker isn't
GitHub. On GitHub, the pull request says `For tracker.ynf-sandbox.invalid/SBX-1.` instead of
`Closes #5.`, and its branch is `ynf/sbx-1`.

Settle it as before:

```bash
ynf sweep --until-settled --timeout 15m --lane gofmt
```

When its CI is green, the ticket's label becomes `ynf:in-review`.

## Work from a prompt

Some work has no ticket anywhere: you just want it done. Start it from a prompt:

```bash
ynf start --prompt "Run gofmt over internal/format" --repo <you>/ynf-sandbox --lane gofmt
echo "exit $?"
```

Expected:

```text
ynf: lane gofmt cannot run adhoc/01m43k6p2c9w4y7t1h5r0x8e3b: no pkg: label for {label.pkg}; give it with --label
exit 33
```

The lane's command reads the package from a `pkg:` label, and a prompt has no labels of its own.
ynf checks a lane can run the work before creating anything, for prompts and tickets alike. Give it
the label:

```bash
ynf start --prompt "Run gofmt over internal/format" --label pkg:internal/format \
  --repo <you>/ynf-sandbox --lane gofmt
```

Expected, after the log:

```text
adhoc/01m43k7d5f0b2n6v9q3s8w1y4h: proposed in lane gofmt (proposed as #17)
pull request: https://github.com/<you>/ynf-sandbox/pull/17
```

A prompt becomes an item like any ticket, with the host `adhoc` and a generated id. ynf keeps the
prompt and its labels in its own store, and that's the ticket: the prompt's first line is the title,
the whole prompt is the body. The pull request says `For adhoc/01m43k7d5f0b2n6v9q3s8w1y4h.`, and its
branch is `ynf/adhoc-` and the id's last eight characters. With nowhere to comment or label, the
pull request carries the conversation. You started one of these over HTTP in lesson 5, with
`"labels"` in the request.

## The end of the track

```bash
ynf items ls
gh pr list -R <you>/ynf-sandbox
```

Every pull request ynf opened is a draft, waiting for a person. Merge one and the next sweep moves
its item to `done`; close one and it moves to `closed`, and the lane's yield in `ynf stats` changes
with each. They all fix the same package, so once one is merged the others duplicate it: close
them.

To start again from scratch, `make -C sandbox reset` rebuilds both repositories, and deleting
`~/ynf-tutorial/state.db` forgets everything ynf recorded. `make -C sandbox down` deletes the
repositories.

## What you know now

- A **tracker** that isn't GitHub is declared in the configuration repository, reached through its
  MCP server, and read with CEL `fields`. ynf calls its tools itself, with no model.
- `ynf ticket` shows what ynf reads from a ticket. Check it before relying on a tracker.
- A tracker ticket needs `--repo`, and if it names its repository, the two must agree.
- **Prompts** are work with no ticket. `--label` gives them the labels a lane's runs read.
- Whatever the work came from, it's the same item, the same decisions and the same draft pull
  request.

To connect a real tracker, such as JIRA, follow [Connect a tracker](../../how-to/connect-a-tracker.md).
To bring in agents and memory, continue with the factory track, which picks up from this sandbox.
