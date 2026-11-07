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
      name         = "Develop Branch Protection"
      checks       = ["All Clear"]
      merge_method = "SQUASH"
    }
    main = {
      name         = "Main Branch Protection"
      checks       = ["All Clear", "Verify PR source branch"]
      merge_method = "MERGE"
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

    # Every PR merges through the merge queue, so each commit is checked on top of the queue ahead of
    # it before it lands. A queue merges with the one method its ruleset names: develop squashes, as
    # feature PRs always have; main makes merge commits, as release and hotfix PRs do. The one
    # back-merge per release, main into develop, must be a true merge, so an admin merges it around
    # the queue (the ruleset's admin bypass). The required workflows run on merge_group (ci.yml, protect-main.yml).
    merge_queue {
      merge_method                      = each.value.merge_method
      grouping_strategy                 = "ALLGREEN"
      min_entries_to_merge              = 1
      max_entries_to_merge              = 5
      min_entries_to_merge_wait_minutes = 5
      max_entries_to_build              = 5
      check_response_timeout_minutes    = 60
    }

    required_status_checks {
      strict_required_status_checks_policy = false
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

# A second ruleset no one can bypass, admins included: main and develop can never be deleted or
# force-pushed by accident, whatever the rulesets above allow an admin to do. Created by hand in
# every eyelock YN repository and adopted here (imports.tf).
resource "github_repository_ruleset" "never_delete" {
  repository  = github_repository.ynf.name
  name        = "Never Delete Main or Develop"
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["refs/heads/main", "refs/heads/develop"]
      exclude = []
    }
  }

  rules {
    deletion         = true
    non_fast_forward = true
  }
}
