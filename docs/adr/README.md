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
| [002](002-events-work-items-correlation.md) | One event envelope, work items keyed per ticket or PR, alias keys for correlation, originated and adopted items |
| [003](003-intake-adapters.md) | Webhook, topic, search and internal adapters; webhooks are hints, facts are re-probed |
| [004](004-store-abstraction.md) | A key/value + compare-and-swap store port with SQLite, S3 and DynamoDB providers |
| [005](005-claims-leases-fencing.md) | Exclusive claims: leases with an epoch, heartbeats, fencing, capped retries, restart on reclaim |
| [006](006-decider-and-lane-policy.md) | A fixed state machine in code, lanes in YAML with CEL guards; lanes reference ynh focuses and never redefine them |
| [007](007-executor-and-containment.md) | Executor port; the agent never holds forge write credentials; containment mandatory for unattended lanes |
| [008](008-memory-with-ynm.md) | ynm as shared narrative memory, failure signatures as subjects, memory advisory only |
| [009](009-hosts-and-language.md) | One Go binary, three hosts: daemon, hosted service, CI-native |
| [010](010-governance-and-stop-conditions.md) | Attribution trailers, run capture, retention, stop conditions and auto-pause |
| [011](011-interface-and-observability.md) | The `ynf` CLI, replay, shadow mode, stats, traces |
| [012](012-loose-coupling-and-detection.md) | Runner and memory ports; ynh and ynm are detected providers, never requirements; ynf's own outcome vocabulary |
