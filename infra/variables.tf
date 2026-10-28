variable "project_id" {
  description = "GCP project to deploy into"
  type        = string
  default     = "gcp-sandbox-78tvq4"
}

variable "region" {
  description = "Region - limited to the three regions covered by the GCP free tier"
  type        = string
  default     = "us-central1"

  validation {
    condition     = contains(["us-central1", "us-east1", "us-west1"], var.region)
    error_message = "Use us-central1, us-east1 or us-west1 to stay in the free tier."
  }
}

variable "service_name" {
  description = "Cloud Run service name"
  type        = string
  default     = "hello-web"
}

variable "github_repository" {
  description = "owner/name of the repo allowed to deploy"
  type        = string
  default     = "scottmiller1130/gcp-sandbox"
}

variable "github_repository_id" {
  description = "Numeric GitHub repo ID. Trust is pinned to this, not the name, so a deleted-and-recreated repo with the same name can't deploy."
  type        = string
  default     = "1437300551"
}

variable "max_instances" {
  description = "Upper bound on instances. This is the main cost guardrail - keep it low."
  type        = number
  default     = 10

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

variable "bootstrap_image" {
  description = "Image used only when the service is first created. CI owns the image after that."
  type        = string
  default     = "us-docker.pkg.dev/cloudrun/container/hello"
}
