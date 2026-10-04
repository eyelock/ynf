terraform {
  required_version = ">= 1.10"
  required_providers {
    github = {
      source  = "integrations/github"
      version = "~> 6.6"
    }
  }

  # State is local (terraform.tfstate here) unless sandbox.env sets SANDBOX_STATE=s3: then `make
  # init` writes backend_override.tf with the S3 backend it names.
}

# The token comes from GITHUB_TOKEN; the Makefile sets it from `gh auth token`.
provider "github" {
  owner = var.owner
}
