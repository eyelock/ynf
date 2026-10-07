variable "owner" {
  description = "GitHub account that owns the repository"
  type        = string
  default     = "eyelock"
}

variable "repository" {
  description = "Repository name"
  type        = string
  default     = "ynf"
}

variable "visibility" {
  description = "Repository visibility. ynf is public, so this defaults to public: secret scanning, push protection and private vulnerability reporting depend on it"
  type        = string
  default     = "public"

  validation {
    condition     = contains(["private", "public"], var.visibility)
    error_message = "visibility must be private or public."
  }
}

variable "release_token" {
  description = <<-EOT
    RELEASE_TOKEN for the release workflow: a token that can write releases to eyelock/ynf and push
    to eyelock/homebrew-tap. Only needed if Terraform creates the secret; normally it is set with
    `gh secret set` and imported, and an existing value is never read back.
  EOT
  type        = string
  sensitive   = true
  default     = null
}
