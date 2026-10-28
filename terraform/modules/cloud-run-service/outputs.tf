output "name" {
  value = google_cloud_run_v2_service.this.name
}

output "location" {
  value = google_cloud_run_v2_service.this.location
}

output "uri" {
  value = google_cloud_run_v2_service.this.uri
}

output "runtime_service_account" {
  value = google_service_account.runtime.email
}

output "admin_token_secret" {
  value = google_secret_manager_secret.admin_token.secret_id
}
