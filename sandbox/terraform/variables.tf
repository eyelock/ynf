variable "owner" {
  description = "GitHub user or organisation that owns the sandbox."
  type        = string
  default     = "eyelock"
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
