# ADR-003: Intake adapters

Status: draft (2026-10-03)
Satisfies: FR-1, FR-2, FR-3, FR-4, FR-5, NFR-5

## Context

Work arrives in two ways: something tells ynf (a webhook, a message on a topic) or ynf goes and
looks (a scheduled search). Webhook deliveries are lost, duplicated and reordered in practice,
and a host may not be reachable from the internet at all (a laptop daemon, some CI setups).

## Decision

**Four adapter families behind one `Source` interface** that emits ADR-002 envelopes.

| Family | Mode | Examples |
|---|---|---|
| Webhook | push | GitHub App, JIRA webhooks |
| Topic | push | SQS/SNS, EventBridge, NATS, Pub/Sub, Kafka |
| Search | pull, scheduled | JQL, GitHub search for issues and pull requests, later Linear |
| Internal | ynf itself | `ynf.run.finished`, `ynf.timer.due`, `ynf.lease.expired` |

**Webhooks are hints, facts are probed.** A step never trusts the event's payload as the current
state. Before deciding, it re-reads what the lane's guards need from the source of truth: check
runs and statuses on the pull request head, review state, labels, ticket status. This is level-
triggered reconciliation, as in Kubernetes controllers: a missed, duplicated or out-of-order
event cannot produce a wrong decision, only a late one.

**Searches are the reconciliation sweep.** Every lane's search runs on its schedule even when
webhooks are wired up. It emits a synthetic event for anything that matches and is not tracked,
and for any tracked item whose probed facts have changed since its last step. A host with no
inbound connectivity runs on searches alone, with a longer latency.

**Verify, then shrink, at the edge.** Webhook signatures are verified before anything else
(GitHub HMAC-SHA256, JIRA shared secret or JWT). The raw payload is written to the blob store,
and only the normalised envelope moves inward.

**Dedupe** is a create-only write of `seen/<event id>` with a TTL (7 days by default, 2026-10-03).

**Probes are cached per step,** not across steps, so a decision always sees one consistent
snapshot and the next step sees fresh facts.

## Alternatives

- **Edge-triggered processing of webhook payloads.** Rejected: correct only when every delivery
  arrives exactly once and in order.
- **Polling only.** Simple, and supported, but slow; webhooks remain the low-latency path.
- **Use ynh's `github_check` and `github_status` sensors as the probe.** Kept as the path for
  checks that run *inside* a harness run. ynf's probes run *between* runs and need facts sensors
  do not model (reviews, labels, ticket state), so ynf has its own.

## Consequences

- API rate limits matter: probes are batched per item and searches are paged and spread across
  the schedule window. A GitHub App's installation token is the default credential for this.
- Adding a new provider means writing a `Source` and a `Probe`; the decider does not change.

## Open questions

- Should JIRA probes go through JIRA Cloud's REST API only, or support Data Center as well?

## History

- 2026-10-03: drafted.
