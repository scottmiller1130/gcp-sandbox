# Platform layer: shared services every environment uses.
#
# One registry for all environments: CI builds an image once and promotes the
# same digest dev -> prod, so prod runs exactly the bytes that passed in dev.

terraform {
  required_version = ">= 1.5"

  backend "gcs" {
    bucket = "gcp-sandbox-78tvq4-tfstate"
    prefix = "platform"
  }

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }
}

data "terraform_remote_state" "bootstrap" {
  backend = "gcs"
  config = {
    bucket = "gcp-sandbox-78tvq4-tfstate"
    prefix = "bootstrap"
  }
}

locals {
  bootstrap = data.terraform_remote_state.bootstrap.outputs
}

provider "google" {
  project = local.bootstrap.shared_project_id
  region  = local.bootstrap.region

  default_labels = {
    managed = "terraform"
    layer   = "platform"
  }
}

resource "google_artifact_registry_repository" "apps" {
  repository_id = "apps"
  location      = local.bootstrap.region
  format        = "DOCKER"
  description   = "Container images for sandbox apps (all environments)"

  # Free tier is 0.5 GB: keep enough images for rollbacks in every env, prune the rest.
  cleanup_policy_dry_run = false
  cleanup_policies {
    id     = "keep-recent"
    action = "KEEP"
    most_recent_versions {
      keep_count = 10
    }
  }
  cleanup_policies {
    id     = "delete-old"
    action = "DELETE"
    condition {
      older_than = "1209600s" # 14 days
    }
  }
}

resource "google_artifact_registry_repository_iam_member" "builder_push" {
  repository = google_artifact_registry_repository.apps.name
  location   = google_artifact_registry_repository.apps.location
  role       = "roles/artifactregistry.writer"
  member     = "serviceAccount:${local.bootstrap.ci_builder_email}"
}

# Each environment's Cloud Run service agent pulls images across projects.
resource "google_artifact_registry_repository_iam_member" "env_pull" {
  for_each   = local.bootstrap.environments
  repository = google_artifact_registry_repository.apps.name
  location   = google_artifact_registry_repository.apps.location
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:${each.value.run_service_agent}"
}

output "repository" {
  value = "${google_artifact_registry_repository.apps.location}-docker.pkg.dev/${google_artifact_registry_repository.apps.project}/${google_artifact_registry_repository.apps.repository_id}"
}
