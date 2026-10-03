# Lanes and focuses

ynf has lanes. ynh has focuses and profiles. They sound like the same idea and are not. This page
explains where the line is, and why it matters.

## Inside one run, or across many

A ynh **focus** is a named `{prompt, profile}`: a repeatable way into a harness. A **profile** is
an overlay of hooks, MCP servers, includes and environment. Both decide how the agent behaves
*inside one run*.

A ynf **lane** decides everything *around* runs, for one class of work:

| | ynh focus and profile | ynf lane |
|---|---|---|
| Scope | one `ynh agent run` | the life of a work item, across many runs and days |
| Answers | how does the agent do this task? | whether, when, with which harness, and what next? |
| Holds | prompt, skills, hooks, MCP servers, sensors, budgets | eligibility, routing, retries, escalation, stop conditions |
| Written by | the harness author; portable and published | the operator; your tickets, your labels, your risk appetite |
| Factory layer | harness | runtime |

A lane *points at* a focus. It never restates one:

```yaml
run:
  harness: eyelock/ynh-lint@1.4
  focus: tidy          # what "tidy" means is the harness's business
```

## Where they overlap, and the rule

Two things can be set from both sides: budgets and sensors. `ynh agent run` accepts
`--max-turns` and `--sensor-overlay`, so a lane could override a harness's own choices. If lanes
did that freely, a harness's behaviour would stop being defined by the harness.

So a lane may only **tighten** a budget, never loosen it. ynh records in `budget_sources` who set
each cap, so a lane's tightening shows in the run record. And a lane may only **scope** a sensor
the harness declares, for example narrowing the linter to the package a ticket is about. ynh's
overlay substitutes a command for a declared sensor and refuses one the harness does not declare,
so a lane cannot add, remove or relax a sensor. The real CI result is something ynf checks between
runs, not a sensor inside one.

If a lane needs the agent to behave differently, that is a new focus or profile in the harness,
reviewed where the harness is reviewed.

## Why it matters

The harness is portable: the same harness serves many repositories and many teams. The lane is
local: it encodes your JIRA project, your labels, and how much you trust this class of change.
Keeping them apart means a harness can be improved once and every lane that uses it benefits,
and a lane can be tightened in a crisis without touching anyone else's harness.

See [ADR-006](../adr/006-decider-and-lane-policy.md).
