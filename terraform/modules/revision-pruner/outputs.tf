output "job_name" {
  value = google_cloud_run_v2_job.this.name
}

output "service_account" {
  value = google_service_account.pruner.email
}
