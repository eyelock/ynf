locals {
  root     = abspath("${path.module}/..")
  fixtures = yamldecode(file("${local.root}/fixtures.yaml")).fixtures

  issues = { for f in local.fixtures : f.id => f if f.kind == "issue" }
  prs    = { for f in local.fixtures : f.id => f if f.kind == "pull_request" }

  labels = toset(flatten([for f in local.fixtures : f.labels]))

  # Re-seed when anything under seed/ changes. A change after the first apply needs `make reset`,
  # because main is protected and the seed is a force push.
  seed_hash = sha256(join("", [for p in sort(fileset("${local.root}/seed", "**")) : filesha256("${local.root}/seed/${p}")]))
}

resource "github_repository" "sandbox" {
  name        = var.name
  description = "Disposable fixtures for ynf. Recreated from eyelock/ynf sandbox/ on every reset."
  visibility  = var.visibility

  auto_init              = false
  has_issues             = true
  has_projects           = false
  has_wiki               = false
  allow_merge_commit     = false
  allow_rebase_merge     = false
  allow_squash_merge     = true
  delete_branch_on_merge = true
  archive_on_destroy     = false
}

# One commit with the whole seed, pushed from a local git repository. A file-per-resource seed
# would be one API commit per file, and those race each other on the same branch.
resource "terraform_data" "seed" {
  triggers_replace = [github_repository.sandbox.node_id, local.seed_hash]

  provisioner "local-exec" {
    command = "${local.root}/scripts/seed.sh"
    environment = {
      REPO     = github_repository.sandbox.full_name
      SEED_DIR = "${local.root}/seed"
    }
  }
}

resource "github_issue_label" "label" {
  for_each    = local.labels
  repository  = github_repository.sandbox.name
  name        = each.value
  color       = startswith(each.value, "ynf:") ? "1d5a66" : "c5def5"
  description = startswith(each.value, "ynf:") ? "Routes the item to a ynf lane" : "Scopes ynf's sensors to this package"
}

resource "github_issue" "fixture" {
  for_each   = local.issues
  repository = github_repository.sandbox.name
  title      = each.value.title
  body       = file("${local.root}/${each.value.body}")
  labels     = each.value.labels

  depends_on = [terraform_data.seed, github_issue_label.label]
}

# The branch, its commit and the pull request, opened by the local user so the fixture is a
# pull request ynf did not originate.
resource "terraform_data" "pr" {
  for_each         = local.prs
  triggers_replace = [terraform_data.seed.id]

  provisioner "local-exec" {
    command = "${local.root}/scripts/pr-branch.sh"
    environment = {
      REPO      = github_repository.sandbox.full_name
      BRANCH    = each.value.pull_request.branch
      FILES_DIR = "${local.root}/${each.value.pull_request.files}"
      TITLE     = each.value.title
      BODY_FILE = "${local.root}/${each.value.body}"
      LABELS    = join(",", each.value.labels)
    }
  }

  depends_on = [github_issue_label.label]
}

data "github_repository_pull_requests" "fixture" {
  for_each        = local.prs
  base_repository = github_repository.sandbox.name
  head_ref        = each.value.pull_request.branch
  state           = "open"

  depends_on = [terraform_data.pr]
}

# Protection last: the seed is a push to main, and the fixture branch is cut from it.
resource "github_branch_protection" "main" {
  repository_id    = github_repository.sandbox.node_id
  pattern          = "main"
  enforce_admins   = true
  allows_deletions = false

  allows_force_pushes = false

  required_status_checks {
    strict   = false
    contexts = ["lint", "test", "docs"]
  }

  required_pull_request_reviews {
    required_approving_review_count = 0
  }

  depends_on = [terraform_data.seed, terraform_data.pr]
}
