# ADR-008: Memory with ynm

Status: draft (2026-10-03)
Satisfies: FR-19, FR-20, NFR-3, NFR-8

## Context

A single `ynh agent run` only knows its own trajectory. ynf sees every exit code, sensor verdict,
CI result and review outcome across every item and lane. That makes it the one component able to
notice that the same failure keeps happening. ynm already stores episodic memories with a
`subject`, and its dream pass turns three or more episodic memories with the same subject into a
reflective one.

ynh runs already reach ynm through ynm's ynh plugin (`ynm client install ynh`), which hooks
`on_session_start`, `before_prompt` and `on_stop`. ynm keeps memory in git notes, so a store
shared between machines is a git remote that each one pulls from and pushes to; ynm can also run
as a hosted service.

## Decision

**Who writes what:**

| Writer | What | Type | Namespace and subject |
|---|---|---|---|
| ynh run, via ynm's plugin, when its harness wants memory | in-run learnings and scratch | `working`, promoted to `episodic` | `session/<id>`, the repository's namespace |
| ynf, per occurrence of a failure | one record each time a signature occurs, its text naming the item, run, step and time and what the run reported | `episodic`, `dataSchema: ynf.failure.v1`, tagged `ynf.failure.v1`, `failure` and `occurrence` | `factory/<host>/<org>/<repo>`, subject = the failure signature |
| ynm dream | "this keeps happening" | `reflective`, and `procedural` on promote | the same signature subject |

**ynf's own store is the run history; memory holds the patterns.** Every decision, run and action
is already in the item's log in ynf's store (ADR-004): what `ynf replay` reads and what `ynf stats`
counts, by model and effort (ADR-011). ynf does not copy steps into ynm. ynm's consolidation is
built to merge and supersede: its dedupe pass merges episodic memories it judges to state the same
fact, and its contradiction pass, grouping by subject, retires an older memory a newer one
supersedes. Step records sharing an item as their subject ("attempt 1 failed", "attempt 2
converged") would be pruned into a history that was never true. Failure occurrences are what ynm
is for, so ynf writes those, and makes each one's text its own (the item, run, step and time), and
tags it `occurrence`, ynm's reserved tag for one event in a series where repetition is the signal.
The dedupe and contradiction passes leave an occurrence alone, so it is never merged or superseded,
and the reflect pass still counts every one. An older ynm stores the tag as an ordinary tag.

**Failure signatures are the subject.** A signature is deterministic and normalised, so the same
failure clusters without a model:

```
sig/ci-diverges/golangci-lint                    converged in the loop, failed in real CI
sig/ci/golangci-lint                             failed in CI where the loop had not converged first
sig/stuck/sensor:unit-tests                      the run ended with this sensor still failing
sig/budget/turns/harness:local/ynf-sandbox@0.1.0 the harness repeatedly hits its turn cap
sig/outcome/error                                any other failed run, so nothing goes uncounted
sig/egress/denied/registry.npmjs.org             the harness needed a host the lane does not allow
```

Each is built from what the run or the pull request reported. Free text is lower-cased with
spaces turned to `-`, a signature stops at 200 characters (a ynm subject's limit), and one failure
is counted under its specific signatures or, when it has none, under `sig/outcome/<outcome>`, never
both. A run ends on a cap, `turns`, `tokens` or `wall`, as `budget/<cap>/harness:<name>@<version>` (without the harness when the run reported none),
and each sensor still failing is a `stuck` signature of its own. When CI fails, each failed check
is one signature.

`ci-diverges` is the class only ynf can see. It points at drift between the harness's sensors and
the real gate, which is what ynh's `version_command` and `--calibrate` exist to catch.

**Memory is advisory only.** ynm never steers the decider (NFR-3): its recall is ranked and fuzzy,
and reflection needs a model. ynf keeps its own deterministic per-signature counters on the item
and lane documents (ADR-004), incremented from the same failure observations it writes to ynm.
Guards read counters; people and agents read memory.

**Each instance connects to memory itself; nobody relays it.** ynf writes what only it can see,
failures recurring across runs, and reads memory for people. A ynh run whose harness
wants memory connects its agent to ynm through ynm's own client integration, with its own
configuration and credentials. ynf does not put memory into an agent's task: the orchestrator
would otherwise decide what an agent should remember, and a run's task would depend on what the
store held, so the same item could get a different prompt on each attempt.

**How ynf's memory is used:** `ynf stats` lists the top signatures per lane with their reflective
summaries, and a recurring signature can open an issue on the harness's repository proposing a
sensor, a focus change or a quarantine. ynf proposes harness changes; it never makes them. An
agent sees ynf's records only if its harness connects to the same store and namespace.

**A failure record says what failed, and only what the run reported.** The signature says what
kind of failure it was and the decision says what ynf did about it; neither says why, and a
reflect pass over several `sig/outcome/error` occurrences has nothing to explain unless each one
does. So each record's text and `data` carry, where the run reported them: the run's outcome, exit
code and the cap that bound it; the sensors still failing and, for a CI signature, the checks that
failed, by name; the harness name and version; and an excerpt of the failure output, the run's own
detail or error message (a command's exit status and the end of its output, a ynh run's reason).
ynf holds no check's log, so a CI signature names the checks and has no excerpt. The text reads as
a sentence, for ynm's reflection to work over: "Failure `sig/outcome/error` on X in lane `Y`: the
run ended error, exit 3; it reported: "exit 3: the build step failed". Occurrence 3, run `R`.
ynf then decided: outcome.error after 2 retries."

What a record may hold is bounded, because a record at `distributed` level is shared with everyone
who reads that store. Memory may hold content where telemetry may not, but it holds no ticket text
and no prompt: nothing the lane's task was built from, only what the run reported back. The
excerpt is passed through the same secret scrubbing as telemetry (`telemetry.Scrub`: tokens, keys,
credentials in URLs, email addresses) and then cut to its last 1 KiB, scrubbing first so a cut
never leaves half a secret that no longer matches. Lists of names stop at 20. The exit code is
recorded only when the step that writes the record made the run, so a record never claims an exit
code from an earlier run.

**ynf owns its schema.** `ynf.failure.v1` is ynf's, published from this repository as a JSON
schema under `docs/schema/memory/`. ynm stores it as it stores any record: `data` is an object it
does not interpret, and `dataSchema` a label it keeps but does not filter on. ynm's filters are
text, type, level, namespace prefix, subject, tags, a `data` key and time, so each record is also
tagged with its schema, `ynf.failure.v1`, and selected by that tag and the `factory/` namespace.
ynm needs no change, no registry entry and no knowledge of ynf to hold it. The detail is optional
properties only (`outcome`, `exit`, `bound_by`, `failed_sensors`, `failed_checks`, `excerpt`,
`harness`, `harness_version`), so records written before it remain valid `ynf.failure.v1`.

**Loosely coupled, configured by ynf.** ynm is detected with no configuration when installed
(ADR-012), and optional: with no `memory` block ynf runs without it, and only its records and the
reflective summaries go missing. ynf talks to ynm only through its public surfaces, the CLI with
`--json` or the MCP HTTP endpoint, and configures what it needs from its own config rather than
expecting ynm to know about it:

```yaml
memory:
  provider: ynm
  transport: http                   # or cli
  endpoint: https://ynm.internal/mcp
  token_env: YNF_YNM_TOKEN          # a machine token; its subject is the writer in ynm's audit log
  level: distributed                # http's default: a hosted store keeps nothing personal
  namespace: "factory/{repo}"       # {repo} is host/org/repo
  # cwd: /srv/factory/memory        # cli: the folder ynm runs from, which picks its store
```

ynf may drive ynm on its own behalf, such as triggering `memory_consolidate` after a lane's batch
of steps so reflections are current, but only through the same public surfaces, and never by
writing ynm's config files or stores directly. `ynf doctor` reports whether ynm is installed, which is optional (ADR-012).

**Transport, store and level.** On a developer's machine, the CLI with `--json`, run from a folder
whose ynm configuration picks the store: their own. Wherever memory is shared, a pool of workers,
a job runner or CI, many writers at once is the case ynm's hosted server is for: ynf writes over
its MCP HTTP endpoint with `memory_remember`, authenticated with a machine token from the identity
provider's client-credentials grant (an Auth0 machine-to-machine application, a Keycloak service
account), whose subject is the writer in ynm's audit log. A git remote that ynm pushes to also
works where writers are few. ynm is personal by default and a hosted store keeps nothing at the
personal level, so a write to a shared store sets `level: distributed` explicitly; over http that is
the default. The server stores the record as ynf sends it: namespace, subject, tags, level,
`dataSchema`, `data` and source arrive unchanged, and `memory_recall` returns them all. ynf always names its namespace, and ynm files only
a record that names none under the caller (`user/<person id>` for a signed-in person, else `common`),
so a static token does not move it. What a token decides is the writer. A per-identity token names
the person or machine in ynm's audit log; a shared static token vouches for no one, and every write
made with it is recorded as `token:static`, the same for every worker. A test runs this against a real
`ynm serve --http` when ynm is on PATH. The factory image carries ynm for the CLI path (ADR-009); the agent's own memory, if
any, is the harness's configuration.

## Alternatives

- **Memory can gate decisions.** More adaptive, but the same event could lead to different
  decisions depending on what the memory store held, which breaks replay.
- **Subject = item only.** Patterns would then cluster per ticket, which almost never recurs; the
  interesting recurrence is across items.

## Consequences

- Signature normalisation is a real piece of code with its own tests: too specific and nothing
  clusters, too broad and unrelated failures merge.
- ynm being down must not stop ynf, or lose a record. A write ynm cannot take is queued in ynf's
  own store, one document per record under `memory/queue/<ULID>`, and sent oldest first before
  the next write and at every sweep. A record is claimed with a conditional write before it is
  sent, so two workers sharing a store never send the same one; a claim left by a dead worker is
  retaken after five minutes. The queue is bounded at 1000 records, dropping the oldest with a
  warning, and a write made while it is not empty goes behind it. `ynf doctor` says how many are
  queued and since when. Recall failures degrade to "no prior context".

## Open questions

- Should the `memory` port admit providers other than ynm? The block has a `provider` key so it
  can, but nothing needs it yet.

## History

- 2026-10-03: drafted.
