resource "github_repository" "ynf" {
  name        = var.repository
  description = "Your named factory: the event-driven outer loop around agent runs. Intake, lanes, claims, containment."
  visibility  = var.visibility
  topics = [
    "agent-orchestration",
    "ai-agents",
    "factory",
    "github-automation",
    "golang",
    "jira",
  ]

  has_issues      = true
  has_discussions = false
  has_projects    = false
  has_wiki        = false
  is_template     = false

  # While ynf is being bootstrapped everything lands on main by squash-merged pull request.
  # Gitflow (develop and release branches, as in ynh and ynm) comes with the first release.
  allow_squash_merge          = true
  allow_merge_commit          = false
  allow_rebase_merge          = false
  allow_auto_merge            = false
  allow_update_branch         = true
  delete_branch_on_merge      = true
  squash_merge_commit_title   = "PR_TITLE"
  squash_merge_commit_message = "PR_BODY"
  web_commit_signoff_required = false

  # A destroy archives the repository instead of deleting it.
  archive_on_destroy = true

  lifecycle {
    prevent_destroy = true
  }
}

# GitHub Pages for docs/ is added once docs/ is on main: the schemas' $id URLs
# (https://eyelock.github.io/ynf/schema/...) resolve from it.
