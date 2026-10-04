resource "github_repository" "ynf" {
  name         = var.repository
  description  = "Your named factory: the event-driven outer loop around agent runs. Intake, lanes, claims, containment."
  homepage_url = "https://eyelock.github.io/ynf/"
  visibility   = var.visibility
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

  # Gitflow (branches.tf): feature PRs are squash-merged into develop, titled and described by the
  # PR, so its "Closes #n" lines close issues; release and hotfix PRs into main are true merges.
  allow_squash_merge          = true
  allow_merge_commit          = true
  allow_rebase_merge          = false
  allow_auto_merge            = false
  allow_update_branch         = true
  delete_branch_on_merge      = true
  squash_merge_commit_title   = "PR_TITLE"
  squash_merge_commit_message = "PR_BODY"
  merge_commit_title          = "MERGE_MESSAGE"
  merge_commit_message        = "PR_TITLE"
  web_commit_signoff_required = false

  # A destroy archives the repository instead of deleting it.
  archive_on_destroy = true

  lifecycle {
    prevent_destroy = true
  }
}

# GitHub Pages for docs/ is added once docs/ is on main: the schemas' $id URLs
# (https://eyelock.github.io/ynf/schema/...) resolve from it.

# The docs site, built by GitHub from /docs on main: docsify renders the Markdown in the browser,
# so there is no build step. It is public even while the repository is private, as ynm's is; the
# JSON schemas' $id URLs resolve here too.
resource "github_repository_pages" "ynf" {
  repository = github_repository.ynf.name
  build_type = "legacy"

  source {
    branch = "main"
    path   = "/docs"
  }
}
