resource "random_id" "suffix" {
  byte_length = 3
}

resource "google_storage_bucket" "sandbox" {
  name          = "${var.project_id}-sandbox-${random_id.suffix.hex}"
  location      = var.region
  storage_class = "STANDARD" # free tier: 5 GB-months of Standard storage

  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = true # lets `terraform destroy` delete a non-empty bucket

  lifecycle_rule {
    condition { age = 30 }
    action { type = "Delete" }
  }
}

resource "google_storage_bucket_object" "hello" {
  bucket  = google_storage_bucket.sandbox.name
  name    = "hello.txt"
  content = "Hello from Terraform!\n"
}
