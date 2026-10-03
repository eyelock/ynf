# Security alerts and automatic security fixes are off, as in ynm.
resource "github_repository_vulnerability_alerts" "ynf" {
  repository = github_repository.ynf.name
  enabled    = false
}

resource "github_repository_dependabot_security_updates" "ynf" {
  repository = github_repository.ynf.name
  enabled    = false
}
