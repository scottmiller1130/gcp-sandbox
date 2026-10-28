resource "google_project_service" "run" {
  service            = "run.googleapis.com"
  disable_on_destroy = false
}

resource "google_cloud_run_v2_service" "hello" {
  name                = "hello"
  location            = var.region
  deletion_protection = false

  template {
    scaling {
      min_instance_count = 0
      max_instance_count = 2
    }
    containers {
      image = "us-docker.pkg.dev/cloudrun/container/hello"
    }
  }

  depends_on = [google_project_service.run]
}

# Make it publicly reachable. Remove this to require authenticated calls.
resource "google_cloud_run_v2_service_iam_member" "public" {
  name     = google_cloud_run_v2_service.hello.name
  location = google_cloud_run_v2_service.hello.location
  role     = "roles/run.invoker"
  member   = "allUsers"
}
