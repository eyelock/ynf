# Why the agent never pushes

In ynf the agent changes files in a worktree and nothing else. ynf commits, pushes, opens the
pull request and comments. This page explains why.

## What the agent reads

A factory's input is tickets and issues, and anyone who can write a ticket can write text the
agent will read. That makes ticket text a way to give the agent instructions nobody intended:
prompt injection. It is not a theoretical problem; it has been demonstrated against coding agents
running in CI.

The useful question is not whether the agent can be talked into something, but what it can do if
it is.

## What it can reach

If the agent holds a token that can push, it can push anywhere the token allows. If it can
comment, approve or merge, so can whoever wrote the ticket. If the network is open, it can send
what it read anywhere.

ynf removes each of those:

- **No write credentials.** The agent gets the model credentials the harness declares, and
  read-only tokens where it needs them. ynf holds the GitHub App and JIRA credentials, and only
  uses them after the run has ended.
- **A diff gate.** Before pushing, ynf checks the diff. A change to sensor configuration,
  baselines, CI workflows, CODEOWNERS, the lane policy, or anything outside the lane's allowed
  paths is escalated, not pushed.
- **Containment.** An unattended lane runs in a container with a deny-by-default network policy.
  A lane that asks for containment the host cannot give fails to load; it never runs
  uncontained instead.

What is left for an injected instruction to achieve is a bad diff, proposed as a pull request, to
a human who has to approve it.

## Why this is ynf's job and not ynh's

ynh is explicit that it declares, executes and packages, and does not enforce containment: "a
well-behaved run and a run that could not misbehave are separate claims". A tool that runs the
agent cannot also be the thing that confines it. ynf is the operator ynh hands that job to.

## What it costs

Harnesses that open their own pull requests with `gh pr create` need a focus that stops at the
diff when run under ynf. And a laptop running unattended lanes needs Docker. Both are on purpose.

See [ADR-007](../adr/007-executor-and-containment.md).
