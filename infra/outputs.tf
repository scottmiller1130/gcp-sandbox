output "service_url" {
  value = google_cloud_run_v2_service.app.uri
}

output "dashboard_url" {
  value = "https://console.cloud.google.com/monitoring/dashboards/builder/${element(split("/", google_monitoring_dashboard.app.id), 3)}?project=${var.project_id}"
}

output "admin_curl" {
  description = "Call /admin with the token from Secret Manager"
  value       = "curl -s -H \"Authorization: Bearer $(gcloud secrets versions access latest --secret ${google_secret_manager_secret.admin_token.secret_id})\" ${google_cloud_run_v2_service.app.uri}/admin | jq"
}

output "run_maintenance_now" {
  value = "gcloud run jobs execute ${google_cloud_run_v2_job.maintenance.name} --region ${var.region} --wait"
}

output "github_variables" {
  description = "Repository variables the workflows read (set with `gh variable set`)"
  value = {
    GCP_PROJECT_ID    = var.project_id
    GCP_REGION        = var.region
    GCP_WIF_PROVIDER  = google_iam_workload_identity_pool_provider.github.name
    GCP_DEPLOYER_SA   = google_service_account.deployer.email
    GCP_AR_REPO       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.apps.repository_id}"
    CLOUD_RUN_SERVICE = google_cloud_run_v2_service.app.name
    CLOUD_RUN_JOB     = google_cloud_run_v2_job.maintenance.name
  }
}
