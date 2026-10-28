# Daily Cloud Run Job that deletes old revisions of one service.
#
#   Cloud Scheduler --(OAuth, scheduler SA)--> Cloud Run Admin API jobs:run
#     --> Job runs "server maintenance" as the pruner SA
#     --> lists/deletes revisions of the service
#
# Free tier: Scheduler gives 3 jobs per *billing account*, shared by every
# project on it. The job runs for a few seconds a day.

terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }
}

locals {
  job_name = "${var.service_name}-maintenance"
}

resource "google_service_account" "pruner" {
  project      = var.project_id
  account_id   = "${var.service_name}-maint"
  display_name = "${var.service_name} maintenance job"
}

resource "google_service_account" "scheduler" {
  project      = var.project_id
  account_id   = "${var.service_name}-scheduler"
  display_name = "Cloud Scheduler trigger for ${local.job_name}"
}

# The job may manage revisions of this one service, nothing else.
resource "google_cloud_run_v2_service_iam_member" "pruner" {
  project  = var.project_id
  name     = var.service_name
  location = var.region
  role     = "roles/run.developer"
  member   = google_service_account.pruner.member
}

resource "google_cloud_run_v2_job" "this" {
  project             = var.project_id
  name                = local.job_name
  location            = var.region
  deletion_protection = false

  template {
    task_count = 1

    template {
      service_account = google_service_account.pruner.email
      max_retries     = 1
      timeout         = "300s"

      containers {
        image = var.bootstrap_image
        args  = ["maintenance"]

        env {
          name  = "GOOGLE_CLOUD_PROJECT"
          value = var.project_id
        }
        env {
          name  = "REGION"
          value = var.region
        }
        env {
          name  = "SERVICE"
          value = var.service_name
        }
        env {
          name  = "KEEP"
          value = tostring(var.keep)
        }

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
        }
      }
    }
  }

  # CI keeps the job on the same image as the service.
  lifecycle {
    ignore_changes = [
      template[0].template[0].containers[0].image,
      template[0].labels,
      template[0].annotations,
      client,
      client_version,
    ]
  }
}

# The scheduler identity can start this job and do nothing else.
resource "google_cloud_run_v2_job_iam_member" "scheduler_runs_job" {
  project  = var.project_id
  name     = google_cloud_run_v2_job.this.name
  location = google_cloud_run_v2_job.this.location
  role     = "roles/run.invoker"
  member   = google_service_account.scheduler.member
}

resource "google_cloud_scheduler_job" "this" {
  project          = var.project_id
  name             = "${local.job_name}-daily"
  region           = var.region
  description      = "Prune old ${var.service_name} revisions"
  schedule         = var.schedule
  time_zone        = var.time_zone
  attempt_deadline = "60s"

  retry_config {
    retry_count = 1
  }

  http_target {
    http_method = "POST"
    uri         = "https://run.googleapis.com/v2/projects/${var.project_id}/locations/${var.region}/jobs/${google_cloud_run_v2_job.this.name}:run"

    oauth_token {
      service_account_email = google_service_account.scheduler.email
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }

  depends_on = [google_cloud_run_v2_job_iam_member.scheduler_runs_job]
}

# CI updates the job's image after each release.
resource "google_cloud_run_v2_job_iam_member" "deployer" {
  project  = var.project_id
  name     = google_cloud_run_v2_job.this.name
  location = google_cloud_run_v2_job.this.location
  role     = "roles/run.developer"
  member   = "serviceAccount:${var.deployer_email}"
}

resource "google_service_account_iam_member" "deployer_act_as_pruner" {
  service_account_id = google_service_account.pruner.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${var.deployer_email}"
}
