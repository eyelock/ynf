# Adopt the live repository into state. On a fresh state these import; once imported they are
# no-ops. To set the repository up from nothing (a new owner or name), delete this file first.
# main has no protection to import yet; the first apply creates it.

import {
  to = github_repository.ynf
  id = "ynf"
}

import {
  to = github_issue_labels.ynf
  id = "ynf"
}

import {
  to = github_actions_repository_permissions.ynf
  id = "ynf"
}

import {
  to = github_workflow_repository_permissions.ynf
  id = "ynf"
}

import {
  to = github_repository_vulnerability_alerts.ynf
  id = "ynf"
}

import {
  to = github_repository_dependabot_security_updates.ynf
  id = "ynf"
}

# Set by hand with gh, so the token never enters Terraform state; this adopts it.
import {
  to = github_actions_secret.release_token
  id = "ynf:RELEASE_TOKEN"
}
