# ADR-004: Store abstraction

Status: draft (2026-10-03)
Satisfies: FR-18, NFR-1, NFR-2

## Context

ynf runs on a developer's machine, on a pool of workers, in a job runner and in CI (ADR-009). Its
state must be SQLite for the first, and an S3 bucket or a DynamoDB table for the others, swappable by configuration.
S3 is the weakest of the three: no queries, no transactions, but since late 2024 it supports
conditional writes (`If-None-Match: *` to create only, `If-Match: <etag>` to replace only the
version you read), which is compare-and-swap.

## Decision

**Design the port down to what S3 can do.** Key/value documents with compare-and-swap, an
append-only log per item, timers, and a blob area. No queries, no cross-key transactions.

```go
type Store interface {
    // Documents (work items, aliases, lane state) with optimistic concurrency.
    Get(ctx context.Context, key string) (doc []byte, version string, err error)
    Put(ctx context.Context, key string, doc []byte, ifVersion string) (string, error) // "" = create only; ErrConflict

    // Append-only per-item log: events, decisions, run results. ULID-keyed, immutable.
    Append(ctx context.Context, item string, entry LogEntry) error
    Log(ctx context.Context, item string, after ulid.ULID) iter.Seq2[LogEntry, error]

    // Timers: the one "query" every host needs.
    Schedule(ctx context.Context, item string, at time.Time) error
    Due(ctx context.Context, now time.Time, limit int) iter.Seq2[string, error]

    // Raw payloads, trajectories, run results.
    Blobs() BlobStore
}
```

Leases (ADR-005) are fields on the item document, written with `Put(…, ifVersion)`, so they
need nothing more from a provider.

| | SQLite | S3 | DynamoDB |
|---|---|---|---|
| Document + CAS | row with a `version` column, `UPDATE … WHERE version = ?` | object, ETag with `If-Match` | item, `ConditionExpression` on `version` |
| Create only | `INSERT` on primary key | `If-None-Match: *` | `attribute_not_exists(pk)` |
| Append log | table keyed `(item, ulid)` | `items/<item>/log/<ulid>.json`, create only | sort key `LOG#<ulid>` |
| Due timers | indexed `due_at` column | marker objects `due/<yyyymmddhhmm>/<item>`, listed by prefix | GSI on a minute bucket of `due_at` |
| Blobs | a directory beside the database | the same bucket under `blobs/` | an S3 bucket configured alongside |

**Timers are hints too.** A due marker that points at an item whose stored `next_due` has moved
is ignored and deleted. The item document is the authority; markers are an index.

**Every step touches one item.** Cross-item consistency is never needed: correlation goes through
create-only aliases (ADR-002), and lane-wide counters (open proposals, yield) are computed from
item documents on a schedule rather than kept transactionally.

**Configuration** is a URL: `sqlite://<home folder>/state.db` (ADR-009), `s3://bucket/prefix?region=…`,
`dynamodb://table?region=…&blobs=s3://bucket/prefix`.

## Alternatives

- **A relational schema with SQLite and Postgres providers.** Easier queries, but rules out S3 and
  fits DynamoDB badly.
- **ynm as the store.** Rejected: ynm is append-only narrative memory with ranked, fuzzy recall,
  and its hosted mode assumes one writer per store (ynm ADR-009). It is ynf's memory, not its
  state (ADR-008).

## Consequences

- `ynf items ls --state running` is a scan in S3. Fine at factory scale (hundreds to low thousands
  of items); a provider can add a secondary index later without changing the port.
- A shared conformance test suite runs against every provider, including concurrent CAS races,
  against LocalStack and DynamoDB Local in CI.

## Open questions

- Should a Postgres provider exist for teams that already run one? Not needed yet.

## History

- 2026-10-03: drafted.
