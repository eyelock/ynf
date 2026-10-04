# ADR-006: The decider and lane policy

Status: draft (2026-10-03)
Satisfies: FR-8, FR-9, FR-10, FR-12, FR-17, NFR-3, NFR-5

## Context

Different repositories and task classes want different rules: which tickets are eligible, which
harness and focus handle them, how many CI failures to retry, what counts as a stop. These rules
must be reviewable, replayable, and hard to change casually, because ynh's factory pattern warns
that stop conditions get "renegotiated under pressure by people who want the programme to
succeed".

ynh already has two concepts that sound similar. A **focus** is a named `{prompt, profile}`, a
repeatable entry point into a harness. A **profile** is an overlay of hooks, MCP servers,
includes and environment passthrough. Both govern what happens *inside* one run.

## Decision

**Hybrid: the machine in code, the policy in YAML.** The state machine and the action vocabulary
are fixed in Go. Lanes are versioned YAML with [CEL](https://cel.dev) guards (cel-go), validated
against [`docs/schema/lanes.schema.json`](../schema/lanes.schema.json) the same way ynh validates its
manifests. The key that maps outcomes to reactions is `when`, not `on`: YAML 1.1 parsers read a
bare `on` as the boolean `true`.

**The state machine:**

```
intake ─► ready ─► running ─► proposed ─► in_review ─► done
  │         ▲         │           │            │
  │         └─retry───┤           ├─ci_failed──┤─changes_requested─► ready (with feedback)
  │                   ├─► escalated   (tamper, operator_error, attempts cap, signature cap)
  └─► ignored         └─► quarantined
```

**The action vocabulary.** Internally the decider emits `run`, `resume_with_feedback`, `open_pr`,
`push_commit` (an adopted item's branch), `comment`, `label`, `transition_ticket`,
`request_review`, `escalate`, `quarantine`, `wait_until` and `close`. A lane names a subset of
these as reactions in its `when` block; the rest follow from the state machine.

**The decider is a pure function:**

```go
func Decide(policy Lane, item Item, facts Facts, ev Event) (Decision, error)
// Decision{ Next State; Actions []Action; WakeAt *time.Time; Reason string }
```

No I/O, no clock (time comes from `ev`), no model calls. Given the same inputs it returns the same
decision, which is what makes `ynf replay` (ADR-011) possible.

**A lane:**

```yaml
lanes:
  lint-paydown:
    kind: originate                      # or: adopt
    intake:
      - github.search: 'repo:eyelock/ynh is:issue is:open label:"ynf:lint"'
        every: 15m
      - tracker: jira                    # a tracker from the config repository (ADR-003)
        search: 'project = PLAT AND labels = ynf-lint'
        every: 15m
    run:
      runner: ynh                        # detected when omitted (ADR-012)
      image: ghcr.io/eyelock/ynh-lint@sha256:…   # a published harness image, pinned
      ynh:
        focus: tidy                      # ynh owns what "tidy" means
        sandbox: srt
        auto_approve: edits              # inside containment only (ADR-007)
        budgets: { max_turns: 20 }       # may only tighten the harness's own
        sensor_scope:                    # narrows declared sensors to the item
          lint: 'golangci-lint run ./{label.pkg}/...'
      executor: docker
    labels:                              # what ynf writes on the ticket, by event
      on_claim:    { add: [ynf:working], remove: [ynf:lint] }
      on_propose:  { add: [ynf:proposed], remove: [ynf:working] }
      on_escalate: { add: [ynf:needs-human], remove: [ynf:working] }
    when:
      converged:         open_pr
      ci_failed:         { retry: 2, then: escalate }
      changes_requested: { resume_with: review_comments, max: 3 }
      outcome.budget:    { retry: 1, then: escalate }
      outcome.stuck:     escalate
      outcome.tamper:    escalate        # never retry
    guards:
      eligible: 'facts.ticket.labels.exists(l, l == "ynf:lint") && !facts.ticket.assignee.human'
    stop:
      yield_floor: 0.15
      max_open_proposals: 5
```

Rules branch on ynf's outcome vocabulary (ADR-012), never on a runner's exit codes.

**Lanes reference ynh, never redefine it.** A lane says *whether, when, with what, and what next*.
The harness, its profile and its focus say *how*. The harness a lane is held to is the one inside
the image that runs it, read from that image (ADR-012), never the repository's working copy. A
lane names a published image, or names a harness folder in the repository that ynf builds into an
image when nothing is published (ADR-007). Two rules keep the line sharp:

- a lane may only **tighten** budgets, never loosen them. ynf compares the lane's budgets with the
  image's harness before the run and refuses a lane that would loosen one; ynh's `budget_sources`
  records who set each cap, so this also shows in the run record
- a lane may **scope** a sensor the image's harness declares, never add, remove or relax one. ynh's
  `--sensor-overlay` substitutes a command for a declared sensor for one run, and rejects a name
  the harness does not declare. A lane uses it to narrow a sensor to the item's part of the
  repository, so the run is judged on the debt it was asked to pay down rather than everyone's.
  Placeholders come only from structured facts (`{label.<prefix>}` reads the value of a
  `<prefix>:<value>` label) and are validated against `^[A-Za-z0-9._/-]+$` before substitution,
  so ticket text never reaches a shell
- the real CI gate is not an overlay: ynf probes it between runs (ADR-003) and decides on it
  itself

If a lane needs different behaviour inside the run, that is a new focus or profile in the
harness.

**Where lanes live: two layers, the repository wins.**

| Layer | Location | Holds |
|---|---|---|
| Config repository | `.agents/factory/` in a repository the operator owns, outside the repositories it works on | enrolment (which repositories ynf works on, on which forge instances), the tracker and forge instances (ADR-003), and defaults: org-wide lanes, retry policy, stop conditions |
| Target repository | `.agents/factory/lanes.yaml` (or a fallback, ADR-009) | overrides and additions to lanes for that repository |

A factory's configuration lives outside the repositories it serves, as a harness can live outside
the repositories it is used on. An instance's own settings (its store, memory, credentials by
variable name, and which config repository to follow) stay in its local configuration (ADR-009).

Lanes merge by name, key by key, and the **target repository wins**: the closer layer knows the
code, the CI and the reviewers. A repository can override any value, add lanes of its own, or
turn a config-repository lane off with `enabled: false`. ynf only reads a target repository's
policy when the config repository enrols it.

Trackers, forges and enrolment are the config repository's alone. They carry credentials, name
servers ynf starts, and decide which repositories ynf touches, so a target repository can refer
to them but never declare or change them.

Repository priority is safe because the guardrails that matter most are not lane configuration.
They are fixed in code and no layer can change them: containment for unattended lanes and no
forge credentials for the agent (ADR-007), the diff gate (ADR-007), never merging (ADR-010),
policy read only from default branches, and lanes only tightening budgets and scoping sensors
relative to the image's harness (above).

The config repository cannot lock keys against repository overrides (default, 2026-10-03). Locks
are added if a real case needs one.

Both layers are read from the **default branch only**, at a resolved commit, never from a pull
request branch, so a pull request cannot rewrite the policy that judges it. A repository-level
policy change is therefore a reviewed merge, which is the deliberate effort ynh's factory pattern
asks for.

`ynf lanes show --repo <host/org/repo> [<lane>]` prints the effective lane with the source of every value
(`config@<sha>` or `repo@<sha>`).

**Every decision records the policy hash**, the SHA-256 of the effective lane's normalised YAML,
plus both source commits, just as ynh records `harness.sha`. A step's log entry is
`{event, facts, policy: {hash, config_sha, repo_sha}, decision}`.

**CEL sees structure, not prose** (NFR-5). The facts exposed to guards are labels, states,
counts, identities and check conclusions. Titles, bodies and comments are not in the CEL
environment.

## Alternatives

- **Policy as Go code registered per lane.** The most flexible, but a policy change is a deploy
  and reviewing it needs a Go reader.
- **A general workflow engine (Temporal, Step Functions).** Durable execution is attractive, but
  it would become a fourth host dependency, and ynf's steps are short and single-item already.
- **Let memory or a model decide.** Rejected by NFR-3 and ADR-008.
- **Config repository wins, or sets ceilings repositories must stay under.** Safer on paper, but
  it puts the people furthest from the code in charge of its details, and the safety it adds is
  already fixed in code.

## Consequences

- New behaviour that the vocabulary cannot express is a code change to ynf, on purpose.
- `ynf lanes validate` and `ynf lanes explain <lane> --item <key>` dry-run a lane against a live
  item without acting.

## Open questions

- Does a config repository need to lock keys (for example a minimum `yield_floor`, or no adoption
  lanes in an org)? Not until a real case appears.

## History

- 2026-10-03: drafted.
