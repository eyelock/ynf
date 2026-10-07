# Measure a lane with shadow mode

Find out what share of a lane's work the agent gets right, before the lane proposes anything.
Shadow mode runs the lane against closed tickets whose fix is already in your history, keeps the
agent's patch beside the human's, and reports the yield with its confidence interval. That yield is
the `y` in the break-even equation, `y* = r / h`.

The method is ynh's: read its
[shadow-mode tutorial](https://github.com/eyelock/ynh/blob/develop/docs/tutorial/shadow-mode.md) first for why
the base state, the task and the harness are chosen as they are, and for what inflates the number:
selection bias, training contamination, toolchain drift, sample size and repository sampling. This
page is only how ynf does it.

## What you need

- ynf [running locally](run-ynf-locally.md), with the repository enrolled and its lane defined.
  The lane can be switched off: shadowing it before it goes on is the use.
- Closed tickets in that repository that a merged pull request fixed, each with a closing
  reference (`Closes #12`), so GitHub recorded which pull request closed it.
- A lane that takes GitHub issues from a `github.search` intake. A lane that adopts pull requests,
  or one whose intake is a tracker search (JIRA, say), is refused for now.
- The lane's containment, as for any run: Docker for a `docker` lane. Shadow mode also accepts a
  `process` lane, run on your machine, as an attended `start` does. No mode opens egress, so the
  lane's `allow` list applies.

## Run the lane

```bash
ynf shadow run lint-paydown --repo example-org/payments --since 90d --limit 20
```

ynf takes the lane's `github.search` intakes and reads them as closed: `is:open` becomes
`is:closed`, with a `closed:>=` date from `--since`. To name tickets yourself, use `--ticket`
(repeatable), which replaces the search:

```bash
ynf shadow run lint-paydown --ticket example-org/payments#412 --ticket example-org/payments#437
```

A ticket is run only if the lane would have taken it and a merged pull request fixed it. The rest
are skipped, and the command says how many and why:

```
shadow run 01K9Q3…: lane lint-paydown, 14 candidate(s), 9 attempted, 5 skipped
  attempt 01K9Q4…: example-org/payments#412 against the fix in #420, converged
  skipped example-org/payments#388: no merged pull request closed it
  skipped example-org/payments#401: the lane cannot run it: no pkg: label for {label.pkg}
```

For each ticket that is run:

- **The base** is the commit before the human fix: the first parent of the fix's merge commit.
  That works for a squash merge and for a merge commit. ynf finds the fix from GitHub's record of
  what closed the ticket (a merged pull request, or a commit that one made), so the pull request must
  have closed it by a closing reference such as `Closes #12`. A fix that was rebase merged has no
  single merge commit, so ynf skips it as "rebase-merged; base unknown" rather than guess.
- **The task** is built from the ticket as `start` builds it: its title, body and labels, never
  the pull request or its commit message, which would give the answer away. ynf's own lifecycle
  labels (a lane's `labels.on_*` additions) are dropped, so the ticket reads as it did before ynf
  touched it.
- **The run** is the lane's, unchanged: runner, harness, focus, budgets, model, effort, executor
  and containment. The checkout is the base commit.
- **The pins** are resolved once, at the start of the first run in each repository, and used for
  every candidate: the lane's policy, and the agent image ynf builds from the harness folder, or
  the harness folder itself on the host. Both come from the repository's default branch, not from
  each old tree, because the harness at an old commit is often a different, older instrument.
  They are recorded with the run.

Nothing leaves the machine. ynf does not push, open a pull request, comment on or label a ticket,
write to memory, or create an item, so `ynf stats` and `ynf items ls` do not change. Each attempt
is stored in ynf's own store under `shadow/<id>/`, which works on the file and S3 stores alike. It
keeps the ticket, the fix pull request, the base, the outcome, the usage, cost, model and effort,
the failed sensors, whether the diff gate would have accepted the change, both patches, and the
run folder with the task, output and trajectory.

`--limit` counts attempts per repository. The run stops early if an attempt ends `operator_error`
(a missing image, an executor that is not allowed): every candidate would end the same way, so fix
it and run again.

```bash
ynf shadow ls      # id, lane, when, candidates, attempted, graded
```

## Grade blind

```bash
ynf shadow grade            # the latest run; or: ynf shadow grade 01K9Q3…
```

At a terminal, for each attempt ynf shows the ticket, then two patches labelled A and B. One is the
agent's and one the human's, in a random order that ynf records but does not show. Give each one of
five grades, by name or number:

| Grade | Meaning |
|---|---|
| `equivalent` | Fixes the same defect by substantially the same means |
| `different-valid` | Fixes the defect by other means. Counts as success |
| `superficial` | Silences the sensor without addressing the defect |
| `wrong` | Changes behaviour incorrectly |
| `does-not-build` | Fails to compile or run |

Only after both are graded does ynf say which was which, and how the run ended. A run that did not
converge but left a patch is still graded blind, and its outcome is shown afterwards.

Grade both patches. The human patch was merged, so it should usually be `equivalent` or
`different-valid`: how often you grade it otherwise is a check on you as a grader, and the report
shows it. Every grade is also a labelled example, should you later want a judge model.

An attempt where the agent left no patch is graded `wrong` automatically, with a note, and is
never shown. Re-grading needs `--regrade`.

A script or a test grades without a terminal, giving the grade of patch A and of patch B in the
attempt's recorded order:

```bash
ynf shadow grade 01K9Q3… --attempt 01K9Q4… --a equivalent --b equivalent
```

## Read the report

```bash
ynf shadow report            # the latest run; or a run id, or --lane <lane> for every run of it
ynf --format json shadow report --lane lint-paydown
```

- **Yield `y`** is (`equivalent` + `different-valid`) divided by the graded agent attempts, with its
  Wilson 95% interval, per repository and pooled. Twelve of twenty is 0.60, with an interval of
  about 0.39 to 0.78: say the interval, not the point.
- **Until attempts are graded**, the report gives an upper bound instead, labelled as one:
  attempts that converged and that the diff gate would have accepted, divided by attempts. It is not a
  yield, and an agent that silences a sensor counts toward it.
- **Superficial** is called out, because an automatic check cannot catch it.
- The outcomes of every attempt, the total cost and the cost per attempt where the runner reports
  one.
- **The pins**: lane, policy hash, harness and its commit, image, ynh version, model and effort.
  If anything differed between attempts, the report says so, because the sample is then more than
  one experiment.
- **The grader check**: how many human patches you graded `equivalent` or `different-valid`.

Lanes do not yet declare the human time `h`, so the report does not compare `y` with
`y* = r / h`. Work it out from the review time `r` in `ynf stats` and your own estimate of `h`.

Treat any shadow yield as an upper bound on what the lane will do on open work. A closed, fixed
ticket is one somebody chose and managed to fix; the ones that were deferred never appear. A
lane whose tickets are older than its model's training cutoff may also be measuring recall:
compare the yield on tickets before and after the cutoff, using `--since` and `--ticket`.

## What it skips

| Skipped | Why |
|---|---|
| the ticket is not closed | the search found it, but it is open again |
| no merged pull request closed it | closed by hand, by a commit with no pull request, or by a pull request that was not merged |
| the lane cannot run it | a `{label.<prefix>}` in the lane's command or sensor scopes has no matching label, or the lane's eligibility guard is false for the ticket as it was |
| rebase-merged; base unknown | the fix was rebase merged, so its merge commit is only the last of its commits |
| the merge commit is not in the clone | the fix's branch was force-pushed or deleted |
| changed no files | the fix pull request had no diff |

A ticket that ynf already worked has lost its trigger label, so a search on that label does not
find it again; name it with `--ticket`.
