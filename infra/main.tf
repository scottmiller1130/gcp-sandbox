data "google_project" "this" {}

locals {
  services = [
    "run.googleapis.com",
    "artifactregistry.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "sts.googleapis.com",
    "monitoring.googleapis.com",
    "secretmanager.googleapis.com",
    "cloudscheduler.googleapis.com",
    "cloudtrace.googleapis.com",
    "clouderrorreporting.googleapis.com",
  ]
}

resource "google_project_service" "enabled" {
  for_each           = toset(local.services)
  service            = each.value
  disable_on_destroy = false
}

# ---------------------------------------------------------------------------
# Image registry
# ---------------------------------------------------------------------------

resource "google_artifact_registry_repository" "apps" {
  repository_id = "apps"
  location      = var.region
  format        = "DOCKER"
  description   = "Container images for sandbox apps"

  # Free tier is 0.5 GB: keep the last few images (enough to roll back), prune the rest.
  cleanup_policy_dry_run = false
  cleanup_policies {
    id     = "keep-recent"
    action = "KEEP"
    most_recent_versions {
      keep_count = 5
    }
  }
  cleanup_policies {
    id     = "delete-old"
    action = "DELETE"
    condition {
      older_than = "604800s" # 7 days
    }
  }

  depends_on = [google_project_service.enabled]
}

# ---------------------------------------------------------------------------
# Service accounts
# ---------------------------------------------------------------------------

# Identity the app runs as. Its only permission is reading its own admin token
# (secrets.tf), so a compromised container can't reach anything else.
resource "google_service_account" "runtime" {
  account_id   = "${var.service_name}-run"
  display_name = "${var.service_name} runtime (no permissions)"
}

# Identity GitHub Actions deploys as.
resource "google_service_account" "deployer" {
  account_id   = "github-deployer"
  display_name = "GitHub Actions deployer for ${var.github_repository}"
}

resource "google_artifact_registry_repository_iam_member" "deployer_push" {
  repository = google_artifact_registry_repository.apps.name
  location   = google_artifact_registry_repository.apps.location
  role       = "roles/artifactregistry.writer"
  member     = google_service_account.deployer.member
}

# Read-only across Cloud Run (needed to poll deploy operations)...
resource "google_project_iam_member" "deployer_run_viewer" {
  project = var.project_id
  role    = "roles/run.viewer"
  member  = google_service_account.deployer.member
}

# ...but can only change this one service.
resource "google_cloud_run_v2_service_iam_member" "deployer_run_developer" {
  name     = google_cloud_run_v2_service.app.name
  location = google_cloud_run_v2_service.app.location
  role     = "roles/run.developer"
  member   = google_service_account.deployer.member
}

# Lets the deployer start revisions that run as the runtime account (and only that one).
resource "google_service_account_iam_member" "deployer_act_as_runtime" {
  service_account_id = google_service_account.runtime.name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.deployer.member
}

# Load-test workflow reads instance counts from Cloud Monitoring.
resource "google_project_iam_member" "deployer_monitoring_viewer" {
  project = var.project_id
  role    = "roles/monitoring.viewer"
  member  = google_service_account.deployer.member
}

# ---------------------------------------------------------------------------
# Keyless GitHub -> GCP auth (Workload Identity Federation)
# ---------------------------------------------------------------------------

resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "github"
  display_name              = "GitHub Actions"
  depends_on                = [google_project_service.enabled]
}

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github-oidc"
  display_name                       = "GitHub OIDC"

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }

  attribute_mapping = {
    "google.subject"          = "assertion.sub"
    "attribute.repository"    = "assertion.repository"
    "attribute.repository_id" = "assertion.repository_id"
    "attribute.ref"           = "assertion.ref"
    "attribute.workflow"      = "assertion.workflow"
    "attribute.actor"         = "assertion.actor"
    "attribute.event_name"    = "assertion.event_name"
  }

  # Only workflows from this repo, running on main, can get a token at all.
  # Pull requests (including from forks) never touch GCP.
  attribute_condition = "assertion.repository_id == \"${var.github_repository_id}\" && assertion.ref == \"refs/heads/main\""
}

resource "google_service_account_iam_member" "github_impersonates_deployer" {
  service_account_id = google_service_account.deployer.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository_id/${var.github_repository_id}"
}

# ---------------------------------------------------------------------------
# The Cloud Run service
# ---------------------------------------------------------------------------

resource "google_cloud_run_v2_service" "app" {
  name                = var.service_name
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

  # After creation, GitHub Actions owns the image, env vars, revision names and
  # traffic split. Terraform owns everything else (scaling, probes, identity).
  lifecycle {
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

  depends_on = [
    google_project_service.enabled,
    # The first revision can't start until the secret exists and is readable.
    google_secret_manager_secret_version.admin_token,
    google_secret_manager_secret_iam_member.runtime_reads_admin_token,
  ]
}

# Public website: anyone may invoke it.
resource "google_cloud_run_v2_service_iam_member" "public" {
  name     = google_cloud_run_v2_service.app.name
  location = google_cloud_run_v2_service.app.location
  role     = "roles/run.invoker"
  member   = "allUsers"
}
