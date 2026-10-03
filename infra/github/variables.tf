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
  description = "Repository visibility: private until the first public release, then public"
  type        = string
  default     = "private"

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
