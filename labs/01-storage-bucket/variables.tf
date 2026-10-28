variable "project_id" {
  description = "GCP project to create resources in"
  type        = string
}

variable "region" {
  description = "Default region"
  type        = string
  default     = "us-central1"
}
