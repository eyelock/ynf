# Connect a tracker

Take on tickets from a tracker that isn't a forge, such as JIRA Cloud or Data Center, through the
tracker's MCP server. ynf calls the server's tools itself, with its own credentials and no model:
reading a ticket, commenting on it and labelling it are plain tool calls.

## Declare it

Trackers are declared only in the factory's configuration repository, in
`.agents/factory/factory.yaml`, never in a target repository: they carry credentials. Name the
tool for each of `get`, `comment` and `label`, and say how to read the `get` tool's result with
CEL `fields`:

```yaml
trackers:
  jira:
    provider: mcp
    site: https://acme.atlassian.net
    server:
      command: [jira-mcp]                  # or url: https://… with token_env
      env: [JIRA_API_TOKEN]                # the only variables it gets, besides PATH and HOME
    get:     { tool: jira_get_issue,   args: { issueKey: "{key}" } }
    comment: { tool: jira_add_comment, args: { issueKey: "{key}", body: "{text}" } }
    label:   { tool: jira_set_labels,  args: { issueKey: "{key}", labels: "{labels}" } }
    fields:
      title: result.fields.summary
      body: result.fields.description
      labels: result.fields.labels
      status: result.fields.status.name
      repo: result.fields.components[0].name    # optional: where the ticket says its code goes
    closed_when: 'status in ["Done", "Won''t Do"]'
```

The tool names and argument names are your server's. Every key is in
[Configuration](../reference/configuration.md#the-configuration-repository).

## Check it

```bash
ynf trackers               # each tracker, and whether its server has the tools named above
ynf ticket jira/PLAT-881   # read one ticket exactly as start would, without starting anything
```

`ynf trackers` starts the server and lists its tools, so a mistyped tool name shows here rather
than in the middle of a run. `ynf ticket` shows the title, state, labels and repository the
`fields` produced: if one is wrong, fix its CEL and run it again.

## Start work on a ticket

```bash
ynf start jira/PLAT-881 --repo acme/payments
```

A tracker ticket doesn't belong to a repository, so `--repo` says where its code goes. If the
ticket names its repository in a structured field (`fields.repo`), the two must agree, which
catches the wrong ticket before anything runs. ynf comments on the ticket with the draft pull
request and labels it as the work moves.
