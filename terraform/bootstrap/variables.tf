variable "shared_project_id" {
  description = "Existing project that holds state, the image registry and CI identities"
  type        = string
  default     = "gcp-sandbox-78tvq4"
}

variable "billing_account" {
  description = "Billing account for new projects (the free tier is shared across all of them). Kept out of the repo: set it in terraform.tfvars (git-ignored) or TF_VAR_billing_account."
  type        = string
}

variable "region" {
  description = "Region for every layer - limited to the three regions covered by the GCP free tier"
  type        = string
  default     = "us-central1"

  validation {
    condition     = contains(["us-central1", "us-east1", "us-west1"], var.region)
    error_message = "Use us-central1, us-east1 or us-west1 to stay in the free tier."
  }
}

variable "environments" {
  description = "Workload environments, each in its own project"
  type        = map(object({ project_id = string }))
  default = {
    dev  = { project_id = "gcp-sandbox-dev-78tvq4" }
    prod = { project_id = "gcp-sandbox-prod-78tvq4" }
  }
}

variable "github_repository" {
  description = "owner/name of the repo CI runs in"
  type        = string
  default     = "scottmiller1130/gcp-sandbox"
}

variable "github_repository_id" {
  description = "Numeric GitHub repo ID. Trust is pinned to this, not the name, so a deleted-and-recreated repo with the same name can't authenticate."
  type        = string
  default     = "1437300551"
}

variable "budget_usd" {
  description = "Monthly budget across all sandbox projects (alerts only - it doesn't cap spend)"
  type        = number
  default     = 1
}
