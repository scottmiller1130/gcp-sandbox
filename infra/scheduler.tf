# Daily Cloud Run Job that deletes old revisions, triggered by Cloud Scheduler.
#
#   Cloud Scheduler --(OAuth, scheduler SA)--> Cloud Run Admin API jobs:run
#     --> Job runs "server maintenance" as the maintenance SA
#     --> lists/deletes revisions of the service
#
# Free tier: Scheduler gives 3 jobs per billing account (this is 1). The job
# runs for a few seconds a day, well inside Cloud Run's free compute.

resource "google_service_account" "maintenance" {
  account_id   = "${var.service_name}-maint"
  display_name = "${var.service_name} maintenance job"
}

resource "google_service_account" "scheduler" {
  account_id   = "${var.service_name}-scheduler"
  display_name = "Cloud Scheduler trigger for ${var.service_name} maintenance"
}

# The job may manage revisions of this one service, nothing else.
resource "google_cloud_run_v2_service_iam_member" "maintenance_prunes_revisions" {
  name     = google_cloud_run_v2_service.app.name
  location = google_cloud_run_v2_service.app.location
  role     = "roles/run.developer"
  member   = google_service_account.maintenance.member
}

resource "google_cloud_run_v2_job" "maintenance" {
  name                = "${var.service_name}-maintenance"
  location            = var.region
  deletion_protection = false

  template {
    task_count = 1

    template {
      service_account = google_service_account.maintenance.email
      max_retries     = 1
      timeout         = "300s"

      containers {
        image = var.bootstrap_job_image
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
          value = google_cloud_run_v2_service.app.name
        }
        env {
          name  = "KEEP"
          value = tostring(var.revisions_to_keep)
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

  # CI keeps the job on the same image as production.
  lifecycle {
    ignore_changes = [
      template[0].template[0].containers[0].image,
      template[0].labels,
      template[0].annotations,
      client,
      client_version,
    ]
  }

  depends_on = [google_project_service.enabled]
}

# The scheduler identity can start this job and do nothing else.
resource "google_cloud_run_v2_job_iam_member" "scheduler_runs_job" {
  name     = google_cloud_run_v2_job.maintenance.name
  location = google_cloud_run_v2_job.maintenance.location
  role     = "roles/run.invoker"
  member   = google_service_account.scheduler.member
}

resource "google_cloud_scheduler_job" "maintenance" {
  name             = "${var.service_name}-maintenance-daily"
  region           = var.region
  description      = "Prune old ${var.service_name} revisions"
  schedule         = var.maintenance_schedule
  time_zone        = var.maintenance_time_zone
  attempt_deadline = "60s"

  retry_config {
    retry_count = 1
  }

  http_target {
    http_method = "POST"
    uri         = "https://run.googleapis.com/v2/projects/${var.project_id}/locations/${var.region}/jobs/${google_cloud_run_v2_job.maintenance.name}:run"

    oauth_token {
      service_account_email = google_service_account.scheduler.email
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }

  depends_on = [google_cloud_run_v2_job_iam_member.scheduler_runs_job]
}

# CI updates the job's image after each release.
resource "google_cloud_run_v2_job_iam_member" "deployer_updates_job" {
  name     = google_cloud_run_v2_job.maintenance.name
  location = google_cloud_run_v2_job.maintenance.location
  role     = "roles/run.developer"
  member   = google_service_account.deployer.member
}

resource "google_service_account_iam_member" "deployer_act_as_maintenance" {
  service_account_id = google_service_account.maintenance.name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.deployer.member
}
