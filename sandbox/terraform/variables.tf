variable "owner" {
  description = "GitHub user or organisation that owns the sandbox: SANDBOX_OWNER in sandbox.env."
  type        = string
}

variable "name" {
  description = "Repository name."
  type        = string
  default     = "ynf-sandbox"
}

variable "visibility" {
  description = "Repository visibility."
  type        = string
  default     = "private"
}

variable "factory_name" {
  description = "The configuration repository's name."
  type        = string
  default     = "ynf-sandbox-factory"
}
