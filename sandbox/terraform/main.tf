locals {
  root     = abspath("${path.module}/..")
  fixtures = yamldecode(file("${local.root}/fixtures.yaml")).fixtures

  issues = { for f in local.fixtures : f.id => f if f.kind == "issue" }
  prs    = { for f in local.fixtures : f.id => f if f.kind == "pull_request" }

  labels = toset(flatten([for f in local.fixtures : f.labels]))

  # A pull request may target a branch of its own, cut from main, when its fixture needs rules that
  # main must not have. The rules below are scoped to those branches alone.
  bases = toset([for f in local.prs : f.pull_request.base if try(f.pull_request.base, "main") != "main"])

  # The seeds name eyelock's sandbox; each is pushed with these names instead. scripts/seed.sh makes
  # the same three replacements, in this order: the factory's name contains the sandbox's.
  sandbox_repo = "${var.owner}/${var.name}"
  factory_repo = "${var.owner}/${var.factory_name}"
  rendered = {
    for dir in ["seed", "factory-seed"] : dir => [
      for p in sort(fileset("${local.root}/${dir}", "**")) : replace(replace(replace(
        file("${local.root}/${dir}/${p}"),
        "eyelock/ynf-sandbox-factory", local.factory_repo),
        "eyelock/ynf-sandbox", local.sandbox_repo),
      "* @eyelock", "* @${var.owner}")
    ]
  }

  # Re-seed when what is pushed changes: a seed file, or the names in it. A change after the first
  # apply needs `make reset`, because main is protected and the seed is a force push.
  seed_hash = sha256(join("", [for c in local.rendered["seed"] : sha256(c)]))

  factory_seed_hash = sha256(join("", [for c in local.rendered["factory-seed"] : sha256(c)]))
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
      REPO         = github_repository.sandbox.full_name
      SEED_DIR     = "${local.root}/seed"
      OWNER        = var.owner
      SANDBOX_REPO = local.sandbox_repo
      FACTORY_REPO = local.factory_repo
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
  body       = replace(replace(file("${local.root}/${each.value.body}"), "eyelock/ynf-sandbox-factory", local.factory_repo), "eyelock/ynf-sandbox", local.sandbox_repo)
  labels     = each.value.labels

  depends_on = [terraform_data.seed, github_issue_label.label]
}

# A base branch for a fixture pull request that needs rules of its own, cut from the seeded main.
resource "github_branch" "base" {
  for_each      = local.bases
  repository    = github_repository.sandbox.name
  branch        = each.value
  source_branch = "main"

  depends_on = [terraform_data.seed]
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
      BASE      = try(each.value.pull_request.base, "main")
      FILES_DIR = "${local.root}/${each.value.pull_request.files}"
      TITLE     = each.value.title
      BODY_FILE = "${local.root}/${each.value.body}"
      LABELS    = join(",", each.value.labels)
    }
  }

  depends_on = [github_issue_label.label, github_branch.base]
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

# A ruleset, not branch protection, requires a check on the gate fixture's base branch, so ynf's
# reading of rulesets is what the fixture proves. No workflow reports gate-never-runs: a required
# check that never starts. The ruleset names that one branch, so it cannot hold the merges e2e makes
# into main (shadow mode merges a human fix there). It is applied after the pull request is open:
# while it is in force the branch takes no direct push, which nothing needs once the fixture is cut.
resource "github_repository_ruleset" "gate" {
  for_each    = local.bases
  repository  = github_repository.sandbox.name
  name        = "required-checks ${each.value}"
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["refs/heads/${each.value}"]
      exclude = []
    }
  }

  rules {
    required_status_checks {
      strict_required_status_checks_policy = false
      required_check {
        context = "gate-never-runs"
      }
    }
  }

  depends_on = [terraform_data.pr]
}

# The factory's configuration repository (ADR-006): enrols the sandbox and gives it default lanes.
resource "github_repository" "factory" {
  name        = var.factory_name
  description = "The ynf sandbox factory's configuration repository. Recreated from eyelock/ynf sandbox/factory-seed on every reset."
  visibility  = var.visibility

  auto_init          = false
  has_issues         = false
  has_projects       = false
  has_wiki           = false
  archive_on_destroy = false
}

resource "terraform_data" "factory_seed" {
  triggers_replace = [github_repository.factory.node_id, local.factory_seed_hash]

  provisioner "local-exec" {
    command = "${local.root}/scripts/seed.sh"
    environment = {
      REPO         = github_repository.factory.full_name
      SEED_DIR     = "${local.root}/factory-seed"
      MESSAGE      = "seed: ynf sandbox factory configuration"
      OWNER        = var.owner
      SANDBOX_REPO = local.sandbox_repo
      FACTORY_REPO = local.factory_repo
    }
  }
}
