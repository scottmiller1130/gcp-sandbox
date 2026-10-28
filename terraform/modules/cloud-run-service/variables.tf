variable "project_id" {
  type = string
}

variable "region" {
  type = string
}

variable "name" {
  description = "Service name (also prefixes the runtime account and secret)"
  type        = string
}

variable "deployer_email" {
  description = "CI service account allowed to release this service"
  type        = string
}

variable "public" {
  description = "Allow unauthenticated requests"
  type        = bool
  default     = true
}

variable "max_instances" {
  description = "Upper bound on instances - the main cost guardrail"
  type        = number

  validation {
    condition     = var.max_instances >= 1 && var.max_instances <= 20
    error_message = "Keep max_instances between 1 and 20 in the sandbox."
  }
}

variable "concurrency" {
  description = "Max concurrent requests per instance before Cloud Run adds another"
  type        = number
  default     = 80
}

variable "admin_token_version" {
  description = "Bump to rotate the /admin token"
  type        = number
  default     = 1
}

variable "bootstrap_image" {
  description = "Image used only when the service is first created"
  type        = string
  default     = "us-docker.pkg.dev/cloudrun/container/hello"
}
