output "bucket" {
  value = google_storage_bucket.sandbox.name
}

output "try_this" {
  value = "gcloud storage cat gs://${google_storage_bucket.sandbox.name}/hello.txt"
}
