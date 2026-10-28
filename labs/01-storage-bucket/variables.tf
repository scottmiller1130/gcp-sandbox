variable "project_id" {
  description = "GCP project to create resources in"
  type        = string
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
