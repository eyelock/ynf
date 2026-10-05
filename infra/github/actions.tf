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

# GitHub never returns a secret's value, so Terraform owns that the secret exists and writes the
# value only when it creates it; after that the value is ignored. It is set with
# `gh secret set RELEASE_TOKEN -R eyelock/ynf` and adopted by the import in imports.tf, so the
# token never passes through Terraform or its state. To rotate it, set it again with gh.
data "github_actions_secrets" "ynf" {
  name = github_repository.ynf.name
}

resource "github_actions_secret" "release_token" {
  repository  = github_repository.ynf.name
  secret_name = "RELEASE_TOKEN"
  # The placeholder is never written: the precondition stops a create without a real value, and
  # ignore_changes stops an update.
  value = coalesce(var.release_token, "unset")

  lifecycle {
    ignore_changes = [value]

    precondition {
      condition     = var.release_token != null || contains(data.github_actions_secrets.ynf.secrets[*].name, "RELEASE_TOKEN")
      error_message = "RELEASE_TOKEN does not exist yet: set it with `gh secret set RELEASE_TOKEN -R eyelock/ynf` (see README.md), then plan again."
    }
  }
}

# YNR_READ_PACKAGES reads eyelock/ynr while it is private: its Go module, fetched over git, and its
# npm packages. Builds and tests fetch ynr with it; RELEASE_TOKEN stays for releases and the tap.
# Set and adopted the same way as RELEASE_TOKEN.
resource "github_actions_secret" "ynr_read_packages" {
  repository  = github_repository.ynf.name
  secret_name = "YNR_READ_PACKAGES"
  value       = coalesce(var.ynr_read_packages, "unset")

  lifecycle {
    ignore_changes = [value]

    precondition {
      condition     = var.ynr_read_packages != null || contains(data.github_actions_secrets.ynf.secrets[*].name, "YNR_READ_PACKAGES")
      error_message = "YNR_READ_PACKAGES does not exist yet: set it with `gh secret set YNR_READ_PACKAGES -R eyelock/ynf` (see README.md), then plan again."
    }
  }
}
