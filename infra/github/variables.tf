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
