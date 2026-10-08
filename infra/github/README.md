# GitHub repository

Terraform for the `eyelock/ynf` repository's settings, so the repository can be checked for
drift and set up again from nothing. The sandbox repository is not here: it is disposable and
lives in [`sandbox/`](../../sandbox/README.md).

| File | What it manages |
|---|---|
| `repository.tf` | The repository: description, topics, visibility, features (issues, discussions and projects on, wiki off), merge options (squash and merge commits, no rebase), secret scanning and push protection once `visibility` is `public`; and the docs site, GitHub Pages from `/docs` on `main` |
| `branches.tf` | Gitflow: `develop` as the default branch, and one ruleset on each of `develop` ("Develop Branch Protection") and `main` ("Main Branch Protection"): a pull request required with conversations resolved, "All Clear" green, and no need for the branch to be up to date with its base (plus "Verify PR source branch" into `main`, so it takes only `develop`, `release/*` and `hotfix/*`), no force-push or delete, and repository admins able to bypass; and "Never Delete Main or Develop" on both, blocking deletion and force-push with no bypass at all (created by hand, adopted by `imports.tf`). [`.github/BRANCH_PROTECTION.md`](../../.github/BRANCH_PROTECTION.md) describes them |
| `labels.tf` | Issue and PR labels, authoritatively: a label not listed is removed |
| `actions.tf` | Actions permissions, the read-only default `GITHUB_TOKEN`, and that the `RELEASE_TOKEN` secret exists |
| `security.tf` | Dependabot alerts and security updates, and, while `visibility` is `public` (the default), private vulnerability reporting (set with `gh api`, as the provider has no resource for it) |
| `imports.tf` | Import blocks that adopt the live repository into a fresh state |

Private vulnerability reporting, secret scanning and push protection are not on yet: they need a public repository, and switch on when `visibility` is set to `public`.

Not managed here: anything committed to the repository (`.github/`), and the value of `RELEASE_TOKEN`: GitHub never returns it, so
Terraform only tracks that the secret exists.

## The release token

The release workflow publishes the GitHub release and pushes the formula to
`eyelock/homebrew-tap` with `RELEASE_TOKEN`, a fine-grained token limited to those two
repositories with **Contents: Read and write**. Set or rotate it with gh, so the value never
passes through Terraform:

```bash
gh secret set RELEASE_TOKEN -R eyelock/ynf
```

`imports.tf` adopts it into state on the next apply.

## Use

Needs Terraform 1.10 or later, the `gh` CLI (for private vulnerability reporting), a GitHub token with the `repo` and `workflow` scopes, and the
`ynf-terraform` AWS profile. State is in S3 at
`s3://ynf-terraform-state.eyelock.net/github/terraform.tfstate`, locked with a `.tflock` object
beside it; the bucket comes from [`../terraform-state`](../terraform-state/README.md). It holds
no secrets, and if it is ever lost the import blocks rebuild it from the live repository.

```bash
cd infra/github
export AWS_PROFILE=ynf-terraform
export GITHUB_TOKEN="$(gh auth token)"
terraform init
terraform plan     # no changes means the repository matches this configuration
terraform apply
```

The rulesets require the "All Clear" job in `.github/workflows/ci.yml`, so merge the change that adds it before applying them: a required check that never reports blocks every pull request. Applying replaces the classic branch protection on `develop` and `main` with the rulesets in one run.

To change a setting, edit the `.tf` file, `plan`, then `apply`. A change made in the GitHub UI
shows as drift in the next `plan`; either copy it into the configuration or `apply` to undo it.

The repository has `prevent_destroy` and `archive_on_destroy`, so `terraform destroy` stops,
and removing that guard archives the repository rather than deleting it.

## Set up from nothing

For a new owner or name, set `owner` and `repository`, then:

1. Delete `imports.tf`: there is nothing to import.
2. Create only the repository: `terraform apply -target=github_repository.ynf`.
3. Push `main` and `develop` from a clone. The protection and the default branch need them to exist.
4. Apply the rest: `terraform apply`.
