# ynf

**Your named factory.** The outer loop around [ynh](https://github.com/eyelock/ynh) agent runs:
it watches tickets, pull requests and message topics, decides deterministically what the next
turn should be, runs it in a container (with `ynh agent run` when ynh is installed), and remembers
what keeps going wrong (in [ynm](https://github.com/eyelock/ynm) when it is installed).

> ynf is at the design stage. What is here is the design: the decisions and the reasoning behind
> them. Tutorials, how-to guides and reference pages arrive with the code.

ynh manages how an agent is guided and runs one bounded, resumable loop against your sensors. ynm
manages what agents remember. Neither owns what happens *between* runs: noticing a ticket is
ready, watching the pull request the run led to, reacting to red CI or a review, and deciding
whether to try again, ask a human, or stop. ynh's factory pattern calls that the runtime layer and
leaves it to the operator. ynf is that operator.

## What is here

This documentation follows [Diátaxis](https://diataxis.fr), as ynm's does: four kinds of page,
each with one job. Two exist so far.

| | |
|---|---|
| [Explanation](explanation/README.md) | Why ynf is shaped the way it is, starting with the outer loop. |
| [Architecture decisions](adr/README.md) | Each decision, its alternatives and its consequences, with the requirements they cite. |
| Tutorials | Ordered lessons that double as acceptance tests. With the code. |
| How-to guides and reference | Recipes and facts. With the code. |

## The shape of it

- **A loop around a loop.** ynh runs the inner loop for minutes; ynf runs a durable state machine
  per work item for days.
- **Intake** from webhooks (GitHub, JIRA), message topics, scheduled searches, and ynf's own run,
  timer and lease events. Webhooks are hints; facts are re-probed before every decision.
- **Lanes** are versioned YAML with CEL guards. A lane says whether, when and with which ynh
  harness and focus; the harness says how.
- **One claim at a time.** Leases with an epoch, heartbeats and fencing, over a store that can be
  SQLite, S3 or DynamoDB.
- **The agent never pushes.** It runs contained, with no forge write credentials; ynf commits,
  pushes and opens the pull request.
- **Memory is advisory.** Failure signatures cluster in ynm so people and the next run learn from
  them; decisions run on deterministic counters.
- **One Go binary, three hosts:** a daemon, a hosted service, or a CI job.
- **ynh and ynm are optional.** Installed, they are detected and used with no configuration; absent,
  any command can be the inner loop and memory is simply off.
