# Dependabot alerts and automatic security fixes, and private vulnerability reporting, are on. Secret
# scanning and push protection are on once the repository is public (repository.tf): they are not
# available on a private repository without GitHub Advanced Security.
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
# resource for it, so this calls the API with gh, which reads GITHUB_TOKEN. The call is idempotent;
# it runs on the first apply and again only if the owner or repository name changes.
resource "terraform_data" "private_vulnerability_reporting" {
  triggers_replace = [var.owner, var.repository]

  provisioner "local-exec" {
    command = "gh api -X PUT repos/${var.owner}/${var.repository}/private-vulnerability-reporting"
  }

  depends_on = [github_repository.ynf]
}
