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

variable "admin_token_version" {
  description = "Bump to rotate the /admin token (Terraform generates a new one; it never enters state)"
  type        = number
  default     = 1
}

variable "alert_email" {
  description = "Where alerts are emailed. Leave empty to only show incidents in the console."
  type        = string
  default     = ""
}

variable "maintenance_schedule" {
  description = "Cron schedule for the revision-pruning job"
  type        = string
  default     = "0 4 * * *"
}

variable "maintenance_time_zone" {
  description = "Time zone for maintenance_schedule"
  type        = string
  default     = "America/Denver"
}

variable "revisions_to_keep" {
  description = "Newest revisions the maintenance job always keeps (serving or tagged revisions are kept regardless)"
  type        = number
  default     = 5
}

variable "bootstrap_job_image" {
  description = "Image used only when the maintenance job is first created. CI owns the image after that."
  type        = string
  default     = "us-docker.pkg.dev/cloudrun/container/job"
}

variable "enable_error_rate_alert" {
  description = "5xx-ratio alert. Free until alerting billing starts (no sooner than Sept 2027), then ~$0.35/month."
  type        = bool
  default     = true
}
