# Project factory: one call creates a project, links billing, enables APIs and
# creates the service agents other projects need to grant access to.

terraform {
  required_version = ">= 1.11"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 8.0"
    }
  }
}

variable "project_id" {
  type = string
}

variable "name" {
  type = string
}

variable "billing_account" {
  type = string
}

variable "services" {
  description = "APIs to enable"
  type        = list(string)
}

variable "service_identities" {
  description = "APIs whose service agent must exist up front (e.g. run.googleapis.com, so another project can grant it image pull access)"
  type        = list(string)
  default     = []
}

variable "labels" {
  type    = map(string)
  default = {}
}

resource "google_project" "this" {
  project_id      = var.project_id
  name            = var.name
  billing_account = var.billing_account
  labels          = var.labels

  # Don't create the "default" VPC with its permissive firewall rules.
  auto_create_network = false

  # `terraform destroy` won't delete the project. To tear an environment down,
  # change this to "DELETE" and apply first - a deliberate two-step.
  deletion_policy = "PREVENT"
}

resource "google_project_service" "this" {
  for_each           = toset(var.services)
  project            = google_project.this.project_id
  service            = each.value
  disable_on_destroy = false
}

# Compute Engine's default service account gets roles/editor automatically.
# Nothing here runs as it, so take that away (it keeps existing, with no roles).
resource "google_project_default_service_accounts" "this" {
  count          = contains(var.services, "run.googleapis.com") ? 1 : 0
  project        = google_project.this.project_id
  action         = "DEPRIVILEGE"
  restore_policy = "REVERT_AND_IGNORE_FAILURE"
  depends_on     = [google_project_service.this]
}

resource "google_project_service_identity" "this" {
  provider   = google-beta
  for_each   = toset(var.service_identities)
  project    = google_project.this.project_id
  service    = each.value
  depends_on = [google_project_service.this]
}

output "project_id" {
  value = google_project.this.project_id
}

output "number" {
  value = google_project.this.number
}

output "service_agents" {
  description = "API -> service agent email"
  value       = { for k, v in google_project_service_identity.this : k => v.email }
}
