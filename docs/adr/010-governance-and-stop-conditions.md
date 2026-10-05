# ADR-010: Governance and stop conditions

Status: draft (2026-10-03)
Satisfies: FR-21, FR-22, FR-23, FR-24

## Context

ynh's factory pattern lists governance practices it does not perform and assigns to "whatever
opens the pull request": the attribution trailer, capturing the run result, a retention decision
for trajectories, and stop conditions written down before starting. ynf opens the pull request.

## Decision

**Attribution in the commit message.** Every commit ynf writes carries:

```
Co-Authored-By: <backend/model, when the runner reports one and ynf knows the vendor's address>
YNF-Item: <item key>
YNF-Step: <step_id>
YNF-Run: <run id>
YNH-Session: <session_id from run.json, ynh runner only>
```

ynf knows the address for claude only (`noreply@anthropic.com`); a run by any other vendor carries
no `Co-Authored-By` line, and the other trailers still identify it.

The trailer survives a squash merge where a pull request comment does not, which is what makes the
escaped-defect limit enforceable.

**Run capture.** For each run, ynf stores the run result (ynh's `run.json` with the ynh runner) and
the trajectory in blobs under the step, linked from the item log. Retention is per lane
(`retention: 90d` by default, 2026-10-03), because trajectories can hold source, customer data
and tokens ynh never saw.

The spool files that were not shipped belong to the run capture too (ADR-011): the files a run
wrote into its own folder, `*.jsonl` and `*.open.jsonl`, and ynf's own from `factory/`. They are
copied, not moved, since a spool on a persistent volume is read by the next `ynr serve`, and
ynr removes what it has shipped. A run's are copied under its step, in a `spool/` folder beside its
trajectory, when the run ends and nothing ships them (ynr serve is not running); when it is, at the
end of the job, after `ynr serve` has had its archive time, whatever is still in `runs/` and
`factory/` is copied the same way, a run's into its own, the rest under a folder for the job. Only
regular files are copied, opened without following a link, and the limits are 32 MiB for a run's and
256 MiB for a job's, past which a file stays where it is and the log says so. Spool files hold
telemetry that ynf and ynr already keep content out of, and they follow the lane's retention like
the rest of the capture.

**Stop conditions are lane policy,** in the same reviewed YAML as everything else (ADR-006):

| Condition | Computed from | Effect |
|---|---|---|
| `yield_floor` | merged ÷ proposed over a rolling window | lane paused |
| `review_time_ceiling` | median pull request opened → approved | lane paused |
| `escaped_defects` | reverts or incident links of commits with a `YNF-Item` trailer | lane paused |
| `rubber_stamp` | review duration trending toward zero | lane paused, owners notified |
| `max_open_proposals` | open pull requests from the lane | intake paused until below the cap |

A paused lane keeps tracking open items but starts no new runs. Unpausing is `ynf resume <lane>`,
recorded with who ran it and why.

**A named human approves every merge.** ynf never merges. It may request review from CODEOWNERS.

## Alternatives

- **Stop conditions in a dashboard or alerting system.** Easier to change, which is the problem.

## Consequences

- Yield and review time need a window large enough to mean something. Below a minimum sample
  (20 proposals by default, 2026-10-03) the conditions report "insufficient data" instead of
  pausing or passing.

## Open questions

- How do escaped defects get linked: revert detection only, or an incident tracker integration?

## History

- 2026-10-03: drafted.
