terraform {
  required_version = ">= 1.11"

  backend "gcs" {
    bucket = "gcp-sandbox-78tvq4-tfstate"
    prefix = "bootstrap"
  }

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 8.0"
    }
  }
}

# Budgets and project creation need a quota project; use the shared one.
provider "google" {
  project               = var.shared_project_id
  region                = var.region
  billing_project       = var.shared_project_id
  user_project_override = true

  default_labels = {
    managed = "terraform"
    layer   = "bootstrap"
  }
}

provider "google-beta" {
  project               = var.shared_project_id
  region                = var.region
  billing_project       = var.shared_project_id
  user_project_override = true
}
