terraform {
  required_version = ">= 1.6"
  required_providers {
    github = {
      source  = "integrations/github"
      version = "~> 6.6"
    }
  }
}

# The token comes from GITHUB_TOKEN; the Makefile sets it from `gh auth token`.
provider "github" {
  owner = var.owner
}
