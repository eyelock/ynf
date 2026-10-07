# Gitflow, as in ynh and ynm: develop is the default branch and takes feature PRs; main moves only
# by release or hotfix PRs and carries the release tags. Each is protected by one ruleset, and there
# is no classic branch protection. The branches themselves are git history, pushed from a clone, not
# created here.

resource "github_branch_default" "develop" {
  repository = github_repository.ynf.name
  branch     = "develop"
}

locals {
  # Status checks each protected branch requires before a PR can merge. "All Clear" is the last job
  # of the ci workflow and needs every other job there; "Verify PR source branch" (protect-main.yml)
  # runs only on PRs into main.
  rulesets = {
    develop = {
      name   = "Develop Branch Protection"
      checks = ["All Clear"]
    }
    main = {
      name   = "Main Branch Protection"
      checks = ["All Clear", "Verify PR source branch"]
    }
  }
}

resource "github_repository_ruleset" "this" {
  for_each = local.rulesets

  repository  = github_repository.ynf.name
  name        = each.value.name
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["refs/heads/${each.key}"]
      exclude = []
    }
  }

  rules {
    deletion         = true
    non_fast_forward = true

    # A pull request is required, with no approving review, and its conversations resolved.
    pull_request {
      required_approving_review_count   = 0
      dismiss_stale_reviews_on_push     = false
      require_code_owner_review         = false
      require_last_push_approval        = false
      required_review_thread_resolution = true
    }

    required_status_checks {
      strict_required_status_checks_policy = true
      do_not_enforce_on_create             = false

      dynamic "required_check" {
        for_each = each.value.checks
        content {
          context = required_check.value
        }
      }
    }
  }

  # Repository admins (role 5) can bypass, for emergencies.
  bypass_actors {
    actor_id    = 5
    actor_type  = "RepositoryRole"
    bypass_mode = "always"
  }
}
