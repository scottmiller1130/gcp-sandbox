# An environment's Firestore database: the one place its data is kept.
#
# Protection, from most to least likely to save you:
#   - delete protection: no API call, console click or Terraform run can
#     delete it until someone deliberately turns this off first
#   - prevent_destroy + deletion_policy ABANDON: Terraform refuses to plan a
#     destroy, and even removing it from code only forgets it
#   - nam5 multi-region: every write is synchronously copied to several US
#     regions before it's acknowledged (99.999% availability SLA)
#   - nightly backups to the vault in the shared project (backup.go)
#
# Free tier: only the first database in a project is free (1 GiB stored,
# 50k reads / 20k writes / 20k deletes a day), so this must stay "(default)".
# Point-in-time recovery and managed backups are billed, so they're off.

terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }
}

variable "project_id" {
  type = string
}

variable "location" {
  description = "Multi-region (nam5 = United States). Can't be changed after creation."
  type        = string
  default     = "nam5"
}

variable "app_service_account" {
  description = "The web app's runtime identity (reads and writes the flight log)"
  type        = string
}

variable "job_service_account" {
  description = "The maintenance job's identity (exports for backups, writes for restores)"
  type        = string
}

variable "backup_writer" {
  description = "Vault identity the job borrows to upload backups"
  type        = string
}

resource "google_firestore_database" "this" {
  project     = var.project_id
  name        = "(default)"
  location_id = var.location
  type        = "FIRESTORE_NATIVE"

  delete_protection_state           = "DELETE_PROTECTION_ENABLED"
  point_in_time_recovery_enablement = "POINT_IN_TIME_RECOVERY_DISABLED" # billed
  deletion_policy                   = "ABANDON"

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_project_iam_member" "app" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = "serviceAccount:${var.app_service_account}"
}

resource "google_project_iam_member" "job" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = "serviceAccount:${var.job_service_account}"
}

# The job may act as the vault writer, and nothing else may.
resource "google_service_account_iam_member" "job_borrows_backup_writer" {
  service_account_id = "projects/${var.project_id}/serviceAccounts/${var.backup_writer}"
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${var.job_service_account}"
}

output "database" {
  value = google_firestore_database.this.name
}

output "location" {
  value = google_firestore_database.this.location_id
}
