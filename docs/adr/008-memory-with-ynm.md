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
| ynf, per step | decision, exit code, `bound_by`, failing sensors, CI result, review outcome | `episodic`, `dataSchema: ynf.step.v1` | `factory/<host>/<org>/<repo>`, subject = the item key |
| ynf, per failure | one record per distinct failure in the step | `episodic`, `dataSchema: ynf.failure.v1` | `factory/<host>/<org>/<repo>`, subject = the failure signature |
| ynm dream | "this keeps happening" | `reflective`, and `procedural` on promote | the same signature subject |

**Failure signatures are the subject.** A signature is deterministic and normalised, so the same
failure clusters without a model:

```
sig/ci-diverges/golangci-lint/errcheck          converged in the loop, failed in real CI
sig/stuck/sensor:unit-tests/test:TestFoo        exit 13 on the same failing test
sig/budget/turns/harness:ynh-lint@1.4           repeatedly hits max_turns
sig/review/rejected/lane:lint-paydown           humans keep rejecting this lane's proposals
sig/tamper/baseline                             exit 14
sig/egress/denied/registry.npmjs.org            the harness needed a host the lane does not allow
```

`ci-diverges` is the class only ynf can see. It points at drift between the harness's sensors and
the real gate, which is what ynh's `version_command` and `--calibrate` exist to catch.

**Memory is advisory only.** ynm never steers the decider (NFR-3): its recall is ranked and fuzzy,
and reflection needs a model. ynf keeps its own deterministic per-signature counters on the item
and lane documents (ADR-004), incremented from the same failure observations it writes to ynm.
Guards read counters; people and agents read memory.

**Each instance connects to memory itself; nobody relays it.** ynf writes what only it can see,
outcomes and failures recurring across runs, and reads memory for people. A ynh run whose harness
wants memory connects its agent to ynm through ynm's own client integration, with its own
configuration and credentials. ynf does not put memory into an agent's task: the orchestrator
would otherwise decide what an agent should remember, and a run's task would depend on what the
store held, so the same item could get a different prompt on each attempt.

**How ynf's memory is used:** `ynf stats` lists the top signatures per lane with their reflective
summaries, and a recurring signature can open an issue on the harness's repository proposing a
sensor, a focus change or a quarantine. ynf proposes harness changes; it never makes them. An
agent sees ynf's records only if its harness connects to the same store and namespace.

**ynf owns its schemas.** `ynf.step.v1` and `ynf.failure.v1` are ynf's, published from this
repository as JSON schemas under `docs/schema/memory/`. ynm stores them as it stores any record:
`data` is an object it does not interpret, `dataSchema` a label it filters on. ynm needs no
change, no registry entry and no knowledge of ynf to hold them.

**Loosely coupled, configured by ynf.** ynm is detected with no configuration when installed
(ADR-012), and optional: with no `memory` block ynf runs without it, and only its records and the
reflective summaries go missing. ynf talks to ynm only through its public surfaces, the CLI with
`--json` or the MCP HTTP endpoint, and configures what it needs from its own config rather than
expecting ynm to know about it:

```yaml
memory:
  provider: ynm
  transport: cli                    # or http
  cwd: /srv/factory/memory          # the folder ynm runs from, which picks its store (cli)
  # endpoint: https://ynm.internal/mcp, token_env: YNF_YNM_TOKEN   (http)
  namespace: "factory/{repo}"       # {repo} is host/org/repo
  write: { steps: true, failures: true }
```

ynf may drive ynm on its own behalf, such as triggering `memory_consolidate` after a lane's batch
of steps so reflections are current, but only through the same public surfaces, and never by
writing ynm's config files or stores directly. `ynf doctor` checks the endpoint, the token and
that a test write and recall round-trip.

**Transport and store.** The CLI with `--json`, run from a folder whose ynm configuration picks the
store. On a developer's machine that is their own store. Wherever memory must outlive the process
or be shared, on a worker in a pool, a job runner or CI, the store is a git remote that ynm pulls
from and pushes to, so no service is needed. A hosted `ynm serve --http` is the alternative where
writes should be serialised through one writer (ynm ADR-009). The factory image carries ynm
(ADR-009); the agent's own memory, if any, is the harness's configuration.

## Alternatives

- **Memory can gate decisions.** More adaptive, but the same event could lead to different
  decisions depending on what the memory store held, which breaks replay.
- **Subject = item only.** Patterns would then cluster per ticket, which almost never recurs; the
  interesting recurrence is across items.

## Consequences

- Signature normalisation is a real piece of code with its own tests: too specific and nothing
  clusters, too broad and unrelated failures merge.
- ynm being down must not stop ynf. Writes are queued in the item log and replayed; recall
  failures degrade to "no prior context".

## Open questions

- A shared store through a git remote, written from a container with a token and from several
  workers at once: how ynm reconciles concurrent pushes to the same notes, before it is relied on.
- Should the `memory` port admit providers other than ynm? The block has a `provider` key so it
  can, but nothing needs it yet.

## History

- 2026-10-03: drafted.
