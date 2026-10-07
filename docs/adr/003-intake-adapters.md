# ADR-003: Intake, trackers and forges

Status: draft (2026-10-03)
Satisfies: FR-1, FR-2, FR-3, FR-4, FR-5, NFR-5

## Context

Work arrives in two ways: something tells ynf (an instruction, a webhook, a message on a topic) or
ynf goes and looks (a scheduled search). Webhook deliveries are lost, duplicated and reordered in
practice, and a host may not be reachable from the internet at all (a laptop, some CI setups).

The work and the code live in systems ynf must read and write: where tickets live (GitHub Issues,
JIRA, Linear) and where code lives (GitHub, later GitLab). Each may be the public cloud service or
an organisation's own instance. Finding new work in every one of those systems is a large job of
its own (queries, cursors, paging, rate limits), and one that need not live in ynf.

## Decision

**Told when, gets what.** ynf is told *when* to act on something: an instruction, a webhook, a
message, a timer. It always reads *what* it acts on for itself, from the source of truth, before
deciding. Discovering new work by searching is a convenience a provider may offer, not something
ynf needs from every system: an automation rule, a webhook or a scheduled job can call
`ynf start` instead.

**Five intake families**, each emitting ADR-002 envelopes into the same `step` (ADR-009):

| Family | Mode | Examples |
|---|---|---|
| Instruction | told | `ynf start <ref>`, `ynf start --prompt`, `POST /start` on `ynf serve` |
| Webhook | told | GitHub (github.com and Enterprise Server), JIRA |
| Topic | told | SQS/SNS, EventBridge, NATS, Pub/Sub, Kafka |
| Search | looks, scheduled | GitHub issue and pull request search, a tracker's own query where its provider can search |
| Internal | ynf itself | `ynf.run.finished`, `ynf.timer.due`, `ynf.lease.expired` |

An instruction names exactly one item. ynf resolves its reference (ADR-002), reads the ticket,
checks where its code goes, and creates the item, or steps it if it exists. Every check fails
before anything is created.

**Two ports for the systems ynf works with.** A *tracker* is where work items live; a *forge* is
where code lives. One system can be both, as GitHub is, and ynf still keeps them apart, so a
tracker that is not a forge (JIRA) works with any forge.

| Port | Operations |
|---|---|
| Tracker | `get(key)`: title, body, labels, status and URL; `comment(key, text)`, idempotent per step; `label(key, add, remove)`, idempotent; `search(query)` only where the provider can |
| Forge | pull requests (find, open, read state, checks and reviews), comments on them, branches, files at a ref, the default branch, and pull request search for adopt lanes |

ynf writes to both with its own credentials only (ADR-007). Lanes say which labels mean what, for
example adding `ynf:working` when an item is claimed and removing the label that triggered it, or
adding `ynf:needs-human` on escalation, so ynf hard-codes no label names.

**Providers:**

| Provider | Port | Covers |
|---|---|---|
| `github` | tracker and forge | github.com and GitHub Enterprise Server, by base URL; one client serves both ports |
| `mcp` | tracker | any tracker with an MCP server: JIRA Cloud and Data Center, Linear and others |
| `adhoc` | tracker | work started from a prompt: `get` returns the stored prompt; `comment` and `label` do nothing |

The `mcp` provider calls the server's tools directly, one call per operation, with no model
involved: what it reads is as deterministic and as recordable as a REST call. Configuration names
the server, ynf's own credential variables, the tool for each operation and how to read the
result, as CEL over the tool's JSON:

```yaml
trackers:
  jira:
    provider: mcp
    site: https://example.atlassian.net
    server: { harness: ., name: atlassian }   # a harness's declared server, or a command or URL
    env: [JIRA_API_TOKEN]
    get:     { tool: jira_get_issue,   args: { issueKey: "{key}" } }
    comment: { tool: jira_add_comment, args: { issueKey: "{key}", body: "{text}" } }
    label:   { tool: jira_update_issue, args: { issueKey: "{key}", labels: "{labels}" } }
    fields:
      title:  result.fields.summary
      body:   result.fields.description
      labels: result.fields.labels
      status: result.fields.status.name
    closed_when: 'status in ["Done", "Won''t Do"]'
    repo_field: { from: labels, pattern: '^repo:(?P<repo>[^/]+/[^/]+/[^/]+)$' }
forges:
  github: { provider: github, url: https://github.com }
```

Differences between a tracker's cloud and on-premises editions (APIs, description formats,
authentication) are the MCP server's to handle; each instance points at a server that suits it.
A provider that cannot do what is asked fails loudly: a missing server, an unknown tool, a
`search` the provider lacks, or a ynh without the ability ynf asks it for (ADR-012).

**Webhooks are hints, facts are probed.** A step never trusts the event's payload as the current
state. Before deciding, it re-reads what the lane's guards need from the source of truth: check
runs and statuses on the pull request head, review state, labels, ticket status. This is level-
triggered reconciliation, as in Kubernetes controllers: a missed, duplicated or out-of-order
event cannot produce a wrong decision, only a late one.

**Searches are the reconciliation sweep where a provider can search.** A lane's search runs on its
schedule even when webhooks are wired up. It emits a synthetic event for anything that matches and
is not tracked, and for any tracked item whose probed facts have changed since its last step. A
host with no inbound connectivity runs on searches and timers alone, with a longer latency.

**Verify, then shrink, at the edge.** Webhook signatures are verified before anything else
(GitHub HMAC-SHA256, JIRA shared secret or JWT). The raw payload is written to the blob store,
and only the normalised envelope moves inward. A webhook's instance is read from its payload, so
github.com and an Enterprise Server can share an endpoint.

**Dedupe** is a create-only write of `seen/<event id>` with a TTL (7 days by default, 2026-10-03).

**Probes are cached per step,** not across steps, so a decision always sees one consistent
snapshot and the next step sees fresh facts.

## Alternatives

- **Edge-triggered processing of webhook payloads.** Rejected: correct only when every delivery
  arrives exactly once and in order.
- **Polling only.** Simple, and supported, but slow; webhooks and instructions remain the
  low-latency paths.
- **A search for every tracker inside ynf.** Rejected as a requirement: it makes ynf a general
  poller. Searches stay where a provider offers them; discovery elsewhere stays at the edges.
- **A native client per tracker (JIRA REST, Linear GraphQL).** Possible later behind the same port,
  but the `mcp` provider covers them without tracker-specific code in ynf.
- **Let the agent read and update the ticket through an MCP server.** Kept for context: a harness
  may give its agent a tracker's MCP server to read linked tickets while it works. Rejected for
  intake and write-back: ynf's decisions would rest on an agent's reading, and the agent would hold
  write credentials (ADR-007).
- **Use ynh's `github_check` and `github_status` sensors as the probe.** Kept as the path for
  checks that run *inside* a harness run. ynf's probes run *between* runs and need facts sensors
  do not model (reviews, labels, ticket state), so ynf has its own.

## Consequences

- API rate limits matter: probes are batched per item and searches are paged and spread across
  the schedule window. A GitHub App's installation token is the default credential for this.
- Adding a tracker is configuration when it has an MCP server, and a provider when it does not;
  the decider does not change.
- Trackers and forges are declared in the factory's configuration repository (ADR-006), because
  they carry credentials and commands ynf runs.

## Open questions

- How the `mcp` provider makes its calls: as an MCP client inside ynf, or through
  `ynh mcp call`, which would reuse ynh's knowledge of how a harness's servers start. The port and
  configuration are the same either way.
- JIRA webhooks: which delivery and signing modes to support first.

## History

- 2026-10-03: drafted.
