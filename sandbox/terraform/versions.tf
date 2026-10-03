terraform {
  required_version = ">= 1.10"
  required_providers {
    github = {
      source  = "integrations/github"
      version = "~> 6.6"
    }
  }

  # State bucket from infra/terraform-state, locked with a .tflock object beside the state. Runs need
  # AWS credentials that can use it (the ynf-terraform profile).
  backend "s3" {
    bucket       = "ynf-terraform-state.eyelock.net"
    key          = "sandbox/terraform.tfstate"
    region       = "us-east-1"
    use_lockfile = true
    encrypt      = true
  }
}

# The token comes from GITHUB_TOKEN; the Makefile sets it from `gh auth token`.
provider "github" {
  owner = var.owner
}
