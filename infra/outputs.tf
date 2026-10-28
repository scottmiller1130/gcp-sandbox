output "service_url" {
  value = google_cloud_run_v2_service.app.uri
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
  }
}
