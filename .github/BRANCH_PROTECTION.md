# Branch Protection Configuration

ynf uses Gitflow, so two branches are protected, each by one repository ruleset. There is no
classic branch protection: the rulesets are the single source of truth. They are managed in
Terraform ([`infra/github/branches.tf`](../infra/github/branches.tf)), not by hand.

| Ruleset | Branch | Required checks |
|---|---|---|
| Develop Branch Protection | `develop` (the default branch) | All Clear |
| Main Branch Protection | `main` | All Clear, Verify PR source branch |

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
