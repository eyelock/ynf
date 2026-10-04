# Gitflow, as in ynh and ynm: develop is the default branch and takes feature PRs; main moves only
# by release or hotfix PRs and carries the release tags. Neither takes a direct push, admins
# included. The branches themselves are git history, pushed from a clone, not created here.

resource "github_branch_default" "develop" {
  repository = github_repository.ynf.name
  branch     = "develop"
}

locals {
  # Status checks each protected branch requires before a PR can merge. "check" is the ci
  # workflow's make check; "Verify PR source branch" (protect-main.yml) runs only on PRs into main.
  protected_branches = {
    main    = ["check", "Verify PR source branch"]
    develop = ["check"]
  }
}

resource "github_branch_protection" "this" {
  for_each = local.protected_branches

  repository_id  = github_repository.ynf.node_id
  pattern        = each.key
  enforce_admins = true

  required_status_checks {
    strict   = false
    contexts = each.value
  }

  # A pull request is required, with no approving review: changes go through a PR and green CI.
  required_pull_request_reviews {
    required_approving_review_count = 0
    dismiss_stale_reviews           = false
    require_code_owner_reviews      = false
    require_last_push_approval      = false
  }

  require_signed_commits = false
  # Release and hotfix PRs into main are true merges, so the back-merge into develop is clean.
  required_linear_history         = false
  require_conversation_resolution = false
  allows_force_pushes             = false
  allows_deletions                = false
  lock_branch                     = false
}

# main's protection predates the per-branch map: keep it, rather than delete and recreate it.
moved {
  from = github_branch_protection.main
  to   = github_branch_protection.this["main"]
}
