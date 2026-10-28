# Bearer token for the app's /admin endpoint.
#
# Free tier: 6 active secret versions and 10k accesses a month. Instances read
# the secret once at startup, and rotation replaces the version rather than
# adding to it, so this uses 1 version and a handful of accesses.

resource "google_secret_manager_secret" "admin_token" {
  secret_id = "${var.service_name}-admin-token"

  # One region, so it's one billable location.
  replication {
    user_managed {
      replicas {
        location = var.region
      }
    }
  }

  depends_on = [google_project_service.enabled]
}

# Ephemeral: generated during apply and handed straight to Secret Manager.
# It is never written to the Terraform state file.
ephemeral "random_password" "admin_token" {
  length  = 48
  special = false
}

resource "google_secret_manager_secret_version" "admin_token" {
  secret                 = google_secret_manager_secret.admin_token.id
  secret_data_wo         = ephemeral.random_password.admin_token.result
  secret_data_wo_version = var.admin_token_version

  # On rotation, create the new version before destroying the old one, so
  # instances starting mid-apply always find a token.
  lifecycle {
    create_before_destroy = true
  }
}

# Only the app's runtime identity can read it - and only this secret.
resource "google_secret_manager_secret_iam_member" "runtime_reads_admin_token" {
  secret_id = google_secret_manager_secret.admin_token.id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.runtime.member
}
