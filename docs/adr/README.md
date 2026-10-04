# Architecture Decision Records

These ADRs are **drafts and malleable** until ynf reaches a working product. They follow the same
lifecycle as ynm's:

1. **Draft** (now): each file is edited in place as a decision changes. No supersession chain.
   Defaults chosen without strong evidence are marked `(default, <date>)` in the Decision.
2. **Build**: as the system is built, dated notes go into each ADR's **Addenda** section rather
   than rewriting the Decision, so the trail of what was learned stays visible.
3. **Consolidate**: close to a working product, each ADR's Decision is rewritten to absorb its
   addenda, and the status moves to `accepted`. From then on changes get a new ADR.

Each ADR has: Status, Context, Decision, Alternatives, Consequences, Open questions, History, and
the FR/NFR ids from ADR-000 it satisfies.

| ADR | Decision |
|---|---|
| [000](000-requirements.md) | Functional and non-functional requirements cited by the other ADRs |
| [001](001-positioning-and-the-outer-loop.md) | ynf is the runtime layer of ynh's factory pattern: an event-driven outer loop around `ynh agent run` |
| [002](002-events-work-items-correlation.md) | One event envelope; work items with a ticket and a code reference, keyed by the system's own host and key; alias keys for correlation |
| [003](003-intake-adapters.md) | Told when, gets what: instruction, webhook, topic, search and internal intake; tracker and forge ports with `github` and `mcp` providers |
| [004](004-store-abstraction.md) | A key/value + compare-and-swap store port with SQLite, S3 and DynamoDB providers |
| [005](005-claims-leases-fencing.md) | Exclusive claims: leases with an epoch, heartbeats, fencing, capped retries, restart on reclaim |
| [006](006-decider-and-lane-policy.md) | A fixed state machine in code, lanes in YAML with CEL guards; lanes reference ynh focuses and never redefine them |
| [007](007-executor-and-containment.md) | Executor port; images pulled, built only as a fallback; the agent never holds forge or tracker write credentials; containment mandatory for unattended work |
| [008](008-memory-with-ynm.md) | ynf's store is the run history; ynm holds failure occurrences, by signature, over HTTP for shared stores; advisory only, never relayed into tasks |
| [009](009-hosts-and-language.md) | One Go binary, four hosts: developer machine, worker pool, job runner, CI; ynf publishes the factory image |
| [010](010-governance-and-stop-conditions.md) | Attribution trailers, run capture, retention, stop conditions and auto-pause |
| [011](011-interface-and-observability.md) | The `ynf` CLI (`start` and `handle`), references, replay, shadow mode, stats, logs and traces |
| [012](012-loose-coupling-and-detection.md) | Ports and providers; instances coupled only by contracts; the image is its harness's source of truth; capabilities checked, failing loudly |
