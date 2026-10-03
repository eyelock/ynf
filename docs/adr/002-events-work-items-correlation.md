# ADR-002: Events, work items and correlation

Status: draft (2026-10-03)
Satisfies: FR-5, FR-6, FR-7, NFR-5

## Context

Events arrive from many systems about the same piece of work: a JIRA ticket is labelled, a branch
is pushed, a pull request opens, its checks complete, a reviewer requests changes. ynf has to
recognise all of them as the same work, and decide once per piece of work rather than once per
event.

## Decision

**One envelope.** Every adapter emits a [CloudEvents](https://cloudevents.io) 1.0 event (default,
2026-10-03), so topic transports carry it natively:

```json
{
  "specversion": "1.0",
  "id": "gh-delivery-7f3c…",
  "source": "github/eyelock/ynh",
  "type": "ynf.pr.check_completed",
  "subject": "github:pr:eyelock/ynh#412",
  "time": "2026-10-03T09:14:02Z",
  "data": { "check": "ci/test", "conclusion": "failure", "head_sha": "9c1e…" },
  "ynfrawref": "blob://raw/2026/10/03/gh-delivery-7f3c….json"
}
```

`id` is the provider's delivery id where there is one and a content hash where there is not.
`type` comes from a closed ynf vocabulary (`ynf.ticket.*`, `ynf.pr.*`, `ynf.run.*`,
`ynf.timer.*`, `ynf.lease.*`), not from the provider's own event names. `data` is minimal and
normalised; the raw payload is stored once and referenced.

**Work items.** A work item is one document per unit of work, keyed `item/<kind>/<id>`:
`item/jira/PLAT-881`, `item/github-issue/eyelock/ynh#77`, `item/github-pr/eyelock/ynh#412`. It
holds the lane, the state (ADR-006), counters (attempts, per-signature failure counts), the
current lease (ADR-005), and links to its branch, pull request and runs.

**Alias keys for correlation.** Every external identity that refers to an item gets a create-only
alias: `alias/github:pr:eyelock/ynh#412 → item/jira/PLAT-881`. Correlation resolves the event's
`subject` through aliases. A new alias is written when ynf learns a link from:

1. its own actions (it named the branch `ynf/PLAT-881` and opened the pull request), which is
   authoritative
2. a `YNF-Item: PLAT-881` trailer or a ticket key in the pull request title, for pull requests
   opened by others

Aliases are create-only, so two instances racing to link the same pull request agree on one item.

**Originated and adopted items.** An item is `originated` when ynf created its branch and pull
request, and `adopted` when a lane of type `adopt` matched a pull request someone else opened.
Adoption is opt-in per lane and off by default. An adopted item is keyed by its pull request.

**Untrusted text stays data** (NFR-5). Titles, bodies and comments are carried in `data` only
as fields the policy cannot evaluate as expressions; CEL guards (ADR-006) see labels, states,
counts and identities, never free text. Text reaches the agent only as quoted task context.

## Alternatives

- **Key items by pull request.** Rejected for originated work: the ticket exists before any pull
  request, and one ticket can need a second pull request after a revert.
- **Infer correlation by fuzzy matching.** Rejected: it is nondeterministic and silently wrong.

## Consequences

- An event whose subject resolves to no item either creates one (if a lane's intake matches) or
  is recorded and dropped.
- Adopted items need guards that originated items do not: no force-push, abort if the head moved
  since the probe, skip drafts and forks by default (ADR-007).

## Open questions

- One ticket spanning several repositories: one item with several pull request aliases, or one
  item per repository with a parent link? Deferred until a lane needs it.

## History

- 2026-10-03: drafted.
