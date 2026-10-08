# Branch Protection Configuration

ynf uses Gitflow, so two branches are protected: each by a ruleset of its own, and both by a third
that no one can bypass. There is no classic branch protection: the rulesets are the single source
of truth. They are managed in
Terraform ([`infra/github/branches.tf`](../infra/github/branches.tf)), not by hand.

| Ruleset | Branch | Required checks |
|---|---|---|
| Develop Branch Protection | `develop` (the default branch) | All Clear |
| Main Branch Protection | `main` | All Clear, Verify PR source branch |
| Never Delete Main or Develop | `main` and `develop` | none |

**Never Delete Main or Develop** blocks deleting or force-pushing either branch, and no one can
bypass it, admins included. The two rulesets above let admins bypass in emergencies; this one makes
sure that never extends to losing a branch.

## Required checks

**All Clear** is the last job of the CI workflow. It depends on every other job and fails if any of
them failed or was cancelled. Add or rename CI jobs freely; only All Clear is required for CI.

**Verify PR source branch** is the job in `protect-main.yml`. It runs on pull requests into `main`
and passes only when the source is `develop`, `release/*` or `hotfix/*`.

## Rules, on both branches

- Changes arrive through a pull request, with no approving review required
- All review conversations must be resolved
- The branch need not be up to date with its base before merging (the required checks are not strict)
- Force pushes blocked
- Branch deletion blocked
- Repository admins can bypass in emergencies

## Merging

Feature pull requests into `develop` are squash-merged. Release and hotfix pull requests into
`main` are true merges, so the back-merge into `develop` is clean. Rebase merging is off.
