variable "project_id" {
  type = string
}

variable "region" {
  type = string
}

variable "service_name" {
  description = "Cloud Run service whose revisions are pruned"
  type        = string
}

variable "deployer_email" {
  description = "CI service account allowed to update the job's image"
  type        = string
}

variable "schedule" {
  description = "Cron schedule"
  type        = string
  default     = "0 4 * * *"
}

variable "time_zone" {
  type    = string
  default = "America/Denver"
}

variable "keep" {
  description = "Newest revisions always kept (serving or tagged revisions are kept regardless)"
  type        = number
  default     = 5
}

variable "bootstrap_image" {
  description = "Image used only when the job is first created"
  type        = string
  default     = "us-docker.pkg.dev/cloudrun/container/job"
}
