# Dependabot alerts and automatic security fixes are on. Private vulnerability reporting, secret
# scanning and push protection need a public repository (repository.tf), and follow `visibility`.
resource "github_repository_vulnerability_alerts" "ynf" {
  repository = github_repository.ynf.name
  enabled    = true
}

resource "github_repository_dependabot_security_updates" "ynf" {
  repository = github_repository.ynf.name
  enabled    = true

  # Security updates need the alerts on first.
  depends_on = [github_repository_vulnerability_alerts.ynf]
}

# Private vulnerability reporting is how SECURITY.md asks for reports. The github provider has no
# resource for it, so this calls the API with gh, which reads GITHUB_TOKEN. GitHub refuses the call
# (HTTP 404) on a private repository, so it waits for visibility to be public. The call is
# idempotent; it runs once then, and again only if the owner or repository name changes.
resource "terraform_data" "private_vulnerability_reporting" {
  count = var.visibility == "public" ? 1 : 0

  triggers_replace = [var.owner, var.repository]

  provisioner "local-exec" {
    command = "gh api -X PUT repos/${var.owner}/${var.repository}/private-vulnerability-reporting"
  }

  depends_on = [github_repository.ynf]
}
