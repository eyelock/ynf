# main takes no direct push, admins included: every change is a pull request. No status checks
# are required yet because ynf has no CI; add the job names to required_status_checks when it
# does.

resource "github_branch_protection" "main" {
  repository_id  = github_repository.ynf.node_id
  pattern        = "main"
  enforce_admins = true

  # A pull request is required, with no approving review.
  required_pull_request_reviews {
    required_approving_review_count = 0
    dismiss_stale_reviews           = false
    require_code_owner_reviews      = false
    require_last_push_approval      = false
  }

  require_signed_commits          = false
  required_linear_history         = true
  require_conversation_resolution = false
  allows_force_pushes             = false
  allows_deletions                = false
  lock_branch                     = false
}
