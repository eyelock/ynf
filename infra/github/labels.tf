# Authoritative: a label created in the UI and not listed here is removed on apply. GitHub's
# defaults, the labels Dependabot creates, and the labels the seats use (merge:, priority:, audit, security).
# Colours and descriptions are copied from the live labels, so applying changes nothing.
locals {
  labels = {
    "accessibility"    = { color = "f143ab", description = "Barrier affecting people with disabilities" }
    "bug"              = { color = "d73a4a", description = "Something isn't working" }
    "documentation"    = { color = "0075ca", description = "Improvements or additions to documentation" }
    "duplicate"        = { color = "cfd3d7", description = "This issue or pull request already exists" }
    "enhancement"      = { color = "a2eeef", description = "New feature or request" }
    "good first issue" = { color = "7057ff", description = "Good for newcomers" }
    "help wanted"      = { color = "008672", description = "Extra attention is needed" }
    "invalid"          = { color = "e4e669", description = "This doesn't seem right" }
    "question"         = { color = "d876e3", description = "Further information is requested" }
    "wontfix"          = { color = "ffffff", description = "This will not be worked on" }

    # Labels Dependabot creates.
    "dependencies"   = { color = "0366d6", description = "Pull requests that update a dependency file" }
    "go"             = { color = "16e2e2", description = "Pull requests that update go code" }
    "github_actions" = { color = "000000", description = "Pull requests that update GitHub Actions code" }
    "terraform"      = { color = "5C4EE5", description = "Pull requests that update terraform code" }

    # Labels the review and planning seats read; declared so an apply never deletes them.
    "merge: squash"       = { color = "0E8A16", description = "Merge this pull request with squash" }
    "merge: merge commit" = { color = "5319e7", description = "Merge with a merge commit" }
    "audit: 2026-11"      = { color = "5319e7", description = "Finding of the 2026-11-21 engineering audit" }
    "priority: P1"        = { color = "b60205", description = "Fix in the next patch release" }
    "priority: P2"        = { color = "d93f0b", description = "Fix in the next minor release" }
    "priority: P3"        = { color = "fbca04", description = "Worth doing; next minor or later" }
    "priority: P4"        = { color = "c2e0c6", description = "Low; when convenient" }
    "security"            = { color = "ee0701", description = "Security or privacy" }
  }
}

resource "github_issue_labels" "ynf" {
  repository = github_repository.ynf.name

  dynamic "label" {
    for_each = local.labels
    content {
      name        = label.key
      color       = label.value.color
      description = label.value.description
    }
  }
}
