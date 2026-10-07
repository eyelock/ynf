# ADR-002: Events, work items and correlation

Status: draft (2026-10-03)
Satisfies: FR-5, FR-6, FR-7, NFR-5

## Context

Events arrive from many systems about the same piece of work: a JIRA ticket is labelled, a branch
is pushed, a pull request opens, its checks complete, a reviewer requests changes. ynf has to
recognise all of them as the same work, and decide once per piece of work rather than once per
event.

The work and the code are often in different systems. A GitHub issue and its pull request live in
the same repository, but a JIRA ticket lives in JIRA and its change in a GitHub repository, and
either system may be the public cloud service or an organisation's own instance: github.com or a
GitHub Enterprise Server, JIRA Cloud or JIRA Data Center.

## Decision

**One envelope.** Every adapter emits a [CloudEvents](https://cloudevents.io) 1.0 event (default,
2026-10-03), so topic transports carry it natively:

```json
{
  "specversion": "1.0",
  "id": "gh-delivery-7f3c…",
  "source": "github.com/eyelock/ynh",
  "type": "ynf.pr.check_completed",
  "subject": "github.com/eyelock/ynh#412",
  "time": "2026-10-03T09:14:02Z",
  "data": { "check": "ci/test", "conclusion": "failure", "head_sha": "9c1e…" },
  "ynfrawref": "blob://raw/2026/10/03/gh-delivery-7f3c….json"
}
```

`id` is the provider's delivery id where there is one and a content hash where there is not.
`type` comes from a closed ynf vocabulary (`ynf.ticket.*`, `ynf.pr.*`, `ynf.run.*`,
`ynf.timer.*`, `ynf.lease.*`), not from the provider's own event names. `data` is minimal and
normalised; the raw payload is stored once and referenced. `subject` is a reference, below.

**CloudEvents are the control plane, and telemetry mirrors them one way.** ynf records every
CloudEvent it receives as a short `ynf.intake` span holding one `ynf.intake.received` event, with
the standard `cloudevents.event_id`, `cloudevents.event_source`, `cloudevents.event_type` and
`cloudevents.event_subject` attributes and what ynf did with it: accepted, deduplicated or
rejected. A webhook delivery already received, or refused before it becomes an event, is recorded
the same way. The events a step makes within itself to carry a run's or an action's result are not
received, and show as the step's decisions. The mirror is write-only: nothing in ynf reads
telemetry, and no component turns observation into a CloudEvent, so an agent that can write
telemetry cannot steer the factory with a forged event (ynr ADR-003). The `ynf.intake` span is
what a step's span links to, so an item's history can be followed across steps (ADR-011).

**A work item has two references.** One document per unit of work holds:

| Reference | Says | Example |
|---|---|---|
| Ticket | what the work is: a tracker instance (ADR-003) and that tracker's own key | `{host: example.atlassian.net, key: PLAT-881}` |
| Code | where the change goes: a forge instance, a repository, and once there is one, the branch and pull request | `{host: github.com, repo: eyelock/ynh, pr: 412}` |

It also holds the lane, the state (ADR-006), counters (attempts, per-signature failure counts),
the current lease (ADR-005) and links to its runs. The decider works on facts, not on which
systems these are, so it never changes when a tracker or forge is added.

**Item keys are the system's own host and the tracker's own key**, never a name from
configuration:

| Work | Key | Reference |
|---|---|---|
| A GitHub issue | `item/github.com/eyelock/ynh/issues/77` | `github.com/eyelock/ynh#77` |
| The same on GitHub Enterprise Server | `item/github.example.internal/example-org/x/issues/77` | `github.example.internal/example-org/x#77` |
| An adopted pull request (ADR-007) | `item/github.com/eyelock/ynh/pulls/412` | `github.com/eyelock/ynh#412` |
| A JIRA ticket, Cloud or Data Center | `item/example.atlassian.net/PLAT-881` | `example.atlassian.net/PLAT-881` |
| Work started from a prompt | `item/adhoc/<ulid>` | `adhoc/<ulid>` |

Identity therefore comes from the system that holds the work: a store and its log mean the same
thing on any ynf instance, and renaming a configured instance orphans nothing. People may type
shorthands (`eyelock/ynh#77` for the default GitHub instance, `jira/PLAT-881` for the tracker
configured as `jira`), which ynf resolves to the host form before anything is stored or recorded.

**Ad hoc items** are work started from a prompt rather than a ticket (`ynf start --prompt`,
ADR-011). The prompt is stored on the item and is the task; there is no ticket to comment on or
label, so the draft pull request carries the conversation.

**Where the code goes.** A GitHub issue's code goes to its own repository. Any other item's
repository is given, never inferred from prose:

1. by the person or automation starting it (`ynf start <ref> --repo <host/org/repo>`), or
2. by the lane that found it: a lane lives in a target repository's policy (ADR-006), so a search
   in that lane is a search for that repository's work.

If the tracker is configured with a structured field that names a repository (a label such as
`repo:github.com/example-org/x`, a component or a custom field, matched by a configured pattern), the
ticket must agree with that repository, which catches the wrong ticket or a typo before any run.
Free text is never consulted. The repository must be on a configured forge instance, enrolled,
and reachable with ynf's own credentials. Any failure stops intake before the item is created,
saying which check failed and what it found.

**Alias keys for correlation.** Every external identity that refers to an item gets a create-only
alias: `alias/github.com/eyelock/ynh#412 → item/example.atlassian.net/PLAT-881`. Correlation
resolves the event's `subject` through aliases. A new alias is written when ynf learns a link from:

1. its own actions (it named the branch and opened the pull request), which is authoritative
2. a `YNF-Item: example.atlassian.net/PLAT-881` trailer or a ticket key in the pull request title,
   for pull requests opened by others

Aliases are create-only, so two instances racing to link the same pull request agree on one item.

**Branches** are named from the ticket's key: `ynf/issue-77` for a GitHub issue in its own
repository, `ynf/plat-881` for a JIRA ticket, `ynf/adhoc-<short id>` for a prompt.

**Originated and adopted items.** An item is `originated` when ynf created its branch and pull
request, and `adopted` when a lane of type `adopt` matched a pull request someone else opened.
Adoption is opt-in per lane and off by default. An adopted item is keyed by its pull request,
because there the pull request is the work.

**Untrusted text stays data** (NFR-5). Titles, bodies and comments are carried in `data` only
as fields the policy cannot evaluate as expressions; CEL guards (ADR-006) see labels, states,
counts and identities, never free text. Text reaches the agent only as quoted task context.

## Alternatives

- **Key items by pull request.** Rejected for originated work: the ticket exists before any pull
  request, and one ticket can need a second pull request after a revert.
- **Key items by a configured instance name** (`item/jira/PLAT-881`). Shorter, but identity would
  depend on configuration: renaming an instance, or two factories naming one host differently,
  would silently split or orphan items.
- **Infer the repository from the ticket's text.** Rejected: anyone who can edit a ticket could
  point the factory at another repository, and free text never drives a decision.
- **Infer correlation by fuzzy matching.** Rejected: it is nondeterministic and silently wrong.

## Consequences

- An event whose subject resolves to no item either creates one (if a lane's intake matches) or
  is recorded and dropped.
- Adopted items need guards that originated items do not: no force-push, abort if the head moved
  since the probe, skip drafts and forks by default (ADR-007).
- A tracker and a forge are separate even when one system provides both: GitHub is a tracker for
  its issues and a forge for its repositories (ADR-003).

## Open questions

- One ticket spanning several repositories: one item with several pull request aliases, or one
  item per repository with a parent link? Deferred until a lane needs it.

## History

- 2026-10-03: drafted.
