terraform {
  required_version = ">= 1.11" # write-only attributes keep secrets out of state

  # State lives in GCS (free tier: 5 GB) so it isn't tied to one laptop.
  # Bucket is created once by hand - see docs/cloud-run-app.md.
  backend "gcs" {
    bucket = "gcp-sandbox-78tvq4-tfstate"
    prefix = "cloud-run-app"
  }

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.7"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region

  default_labels = {
    env     = "sandbox"
    managed = "terraform"
  }
}
