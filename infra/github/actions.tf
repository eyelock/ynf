resource "github_actions_repository_permissions" "ynf" {
  repository      = github_repository.ynf.name
  enabled         = true
  allowed_actions = "all"
}

# Workflows get a read-only GITHUB_TOKEN unless a job asks for more, and cannot approve PRs.
resource "github_workflow_repository_permissions" "ynf" {
  repository                       = github_repository.ynf.name
  default_workflow_permissions     = "read"
  can_approve_pull_request_reviews = false
}
