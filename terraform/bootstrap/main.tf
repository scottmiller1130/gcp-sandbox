# Bootstrap layer: everything the other layers stand on.
#
# Applied by a human with owner rights, rarely. CI can read it (for plans) but
# never change it - so CI can't grant itself more access.

data "google_project" "shared" {
  project_id = var.shared_project_id
}

# APIs the shared project needs to act as the "hub" (state, registry, CI identities).
resource "google_project_service" "shared" {
  for_each = toset([
    "cloudresourcemanager.googleapis.com",
    "cloudbilling.googleapis.com",
    "billingbudgets.googleapis.com",
    "serviceusage.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "sts.googleapis.com",
    "artifactregistry.googleapis.com",
    "storage.googleapis.com",
  ])
  project            = var.shared_project_id
  service            = each.value
  disable_on_destroy = false
}

# --- Workload projects ----------------------------------------------------

module "projects" {
  source   = "../modules/project"
  for_each = var.environments

  project_id      = each.value.project_id
  name            = "GCP Sandbox ${each.key}"
  billing_account = var.billing_account
  labels          = { env = each.key, managed = "terraform" }

  services = [
    "run.googleapis.com",
    "iam.googleapis.com",
    "secretmanager.googleapis.com",
    "cloudscheduler.googleapis.com",
    "monitoring.googleapis.com",
    "logging.googleapis.com",
    "cloudtrace.googleapis.com",
    "clouderrorreporting.googleapis.com",
  ]
  # Cloud Run's service agent pulls images from the shared registry, so it has
  # to exist before the platform layer can grant it access.
  service_identities = ["run.googleapis.com"]

  depends_on = [google_project_service.shared]
}

# --- Terraform state bucket (created by hand before Terraform existed) ----

import {
  to = google_storage_bucket.tfstate
  id = "gcp-sandbox-78tvq4-tfstate"
}

resource "google_storage_bucket" "tfstate" {
  project                     = var.shared_project_id
  name                        = "gcp-sandbox-78tvq4-tfstate"
  location                    = "US-CENTRAL1"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  versioning {
    enabled = true
  }

  # Keep 3 old versions of each state file - enough to recover from a bad apply.
  lifecycle_rule {
    condition {
      num_newer_versions = 3
      with_state         = "ARCHIVED"
    }
    action {
      type = "Delete"
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

# --- Budget (created by hand earlier; now covers every sandbox project) ---

import {
  to = google_billing_budget.sandbox
  id = "billingAccounts/01B60E-5EF416-E6C14F/budgets/8c18e023-4d3f-4192-a443-c345563fd22e"
}

resource "google_billing_budget" "sandbox" {
  billing_account = var.billing_account
  display_name    = "sandbox-stay-free"

  budget_filter {
    projects = concat(
      ["projects/${data.google_project.shared.number}"],
      [for p in module.projects : "projects/${p.number}"],
    )
    calendar_period        = "MONTH"
    credit_types_treatment = "INCLUDE_ALL_CREDITS"
  }

  amount {
    specified_amount {
      currency_code = "USD"
      units         = tostring(var.budget_usd)
    }
  }

  threshold_rules {
    threshold_percent = 0.1
  }
  threshold_rules {
    threshold_percent = 0.5
  }
  threshold_rules {
    threshold_percent = 1.0
  }
  threshold_rules {
    threshold_percent = 0.1
    spend_basis       = "FORECASTED_SPEND"
  }

  depends_on = [google_project_service.shared]
}
