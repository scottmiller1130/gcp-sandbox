# A public Cloud Run service, its runtime identity, its admin-token secret,
# and the narrow permissions a CI deployer needs to release it.
#
# After creation, CI owns the image, env vars, revision names and traffic split.
# Terraform owns everything else (scaling, probes, identity, secrets).

resource "google_service_account" "runtime" {
  project      = var.project_id
  account_id   = "${var.name}-run"
  display_name = "${var.name} runtime"
  description  = "Runs ${var.name}. Its only permission is reading its own admin token."
}

# --- Secret ---------------------------------------------------------------

resource "google_secret_manager_secret" "admin_token" {
  project   = var.project_id
  secret_id = "${var.name}-admin-token"

  # One region, so it's one billable location (free tier: 6 active versions).
  replication {
    user_managed {
      replicas {
        location = var.region
      }
    }
  }
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

resource "google_secret_manager_secret_iam_member" "runtime_reads_admin_token" {
  project   = var.project_id
  secret_id = google_secret_manager_secret.admin_token.id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.runtime.member
}

# --- Service --------------------------------------------------------------

resource "google_cloud_run_v2_service" "this" {
  project             = var.project_id
  name                = var.name
  location            = var.region
  ingress             = "INGRESS_TRAFFIC_ALL"
  deletion_protection = false

  template {
    service_account                  = google_service_account.runtime.email
    max_instance_request_concurrency = var.concurrency
    timeout                          = "10s"

    scaling {
      # 0 = no idle cost (free tier). Trade-off: the first request after idle
      # waits for a cold start (~100-300 ms for this image).
      min_instance_count = 0
      max_instance_count = var.max_instances
    }

    containers {
      image = var.bootstrap_image

      ports {
        container_port = 8080
      }

      # Lets the app link its log lines to Cloud Trace.
      env {
        name  = "GOOGLE_CLOUD_PROJECT"
        value = var.project_id
      }

      # Read from Secret Manager when each instance starts. "latest" means a
      # rotated token reaches new instances without a Terraform change.
      env {
        name = "ADMIN_TOKEN"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.admin_token.secret_id
            version = "latest"
          }
        }
      }

      resources {
        limits = {
          cpu    = "1"
          memory = "256Mi"
        }
        cpu_idle          = true # only billed while handling requests
        startup_cpu_boost = false
      }

      # A revision only gets traffic once it answers /healthz...
      startup_probe {
        http_get {
          path = "/healthz"
        }
        period_seconds    = 2
        timeout_seconds   = 1
        failure_threshold = 5
      }

      # ...and an instance that stops answering is replaced.
      liveness_probe {
        http_get {
          path = "/healthz"
        }
        period_seconds    = 15
        timeout_seconds   = 2
        failure_threshold = 3
      }
    }
  }

  lifecycle {
    # Deleting or replacing the service takes the site down. Terraform refuses
    # outright; to really remove it, delete this line in a reviewed PR first.
    prevent_destroy = true

    ignore_changes = [
      template[0].containers[0].image,
      template[0].containers[0].env,
      template[0].revision,
      template[0].labels,
      template[0].annotations,
      traffic,
      client,
      client_version,
    ]
  }

  # The first revision can't start until the secret exists and is readable.
  depends_on = [
    google_secret_manager_secret_version.admin_token,
    google_secret_manager_secret_iam_member.runtime_reads_admin_token,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "public" {
  count    = var.public ? 1 : 0
  project  = var.project_id
  name     = google_cloud_run_v2_service.this.name
  location = google_cloud_run_v2_service.this.location
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# --- Deployer permissions -------------------------------------------------

# The deployer can change this one service...
resource "google_cloud_run_v2_service_iam_member" "deployer" {
  project  = var.project_id
  name     = google_cloud_run_v2_service.this.name
  location = google_cloud_run_v2_service.this.location
  role     = "roles/run.developer"
  member   = "serviceAccount:${var.deployer_email}"
}

# ...and start revisions that run as this runtime account (and only that one).
resource "google_service_account_iam_member" "deployer_act_as_runtime" {
  service_account_id = google_service_account.runtime.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${var.deployer_email}"
}
