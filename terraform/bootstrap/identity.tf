# Who CI is, and what each CI identity may do.
#
#   identity      lives in   usable from    can
#   ------------  ---------  -------------  -----------------------------------------
#   tf-plan       shared     any branch/PR  read everything (plans on pull requests)
#   tf-apply      shared     main only      own dev+prod, manage the shared registry
#   ci-builder    shared     main only      push images (granted in platform layer)
#   ci-deployer   each env   main only      release its env's service (granted in envs)
#
# No identity here can change this bootstrap layer.

resource "google_iam_workload_identity_pool" "github" {
  project                   = var.shared_project_id
  workload_identity_pool_id = "github"
  display_name              = "GitHub Actions"
  description               = "Workflows in ${var.github_repository}"
  depends_on                = [google_project_service.shared]
}

resource "google_iam_workload_identity_pool_provider" "github" {
  project                            = var.shared_project_id
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github-oidc"
  display_name                       = "GitHub OIDC"

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }

  attribute_mapping = {
    "google.subject"          = "assertion.sub"
    "attribute.repository"    = "assertion.repository"
    "attribute.repository_id" = "assertion.repository_id"
    "attribute.ref"           = "assertion.ref"
    "attribute.workflow"      = "assertion.workflow"
    "attribute.actor"         = "assertion.actor"
    # Lets a binding require "this repo AND this branch" in one principalSet.
    "attribute.repo_ref" = "assertion.repository_id + ':' + assertion.ref"
  }

  # Only this repository can get a token at all.
  attribute_condition = "assertion.repository_id == \"${var.github_repository_id}\""
}

locals {
  pool            = google_iam_workload_identity_pool.github.name
  from_any_branch = "principalSet://iam.googleapis.com/${local.pool}/attribute.repository_id/${var.github_repository_id}"
  from_main       = "principalSet://iam.googleapis.com/${local.pool}/attribute.repo_ref/${var.github_repository_id}:refs/heads/main"
}

# --- Terraform: plan (read-only, PRs) -------------------------------------

resource "google_service_account" "tf_plan" {
  project      = var.shared_project_id
  account_id   = "tf-plan"
  display_name = "Terraform plan (read-only)"
}

resource "google_service_account_iam_member" "tf_plan_wif" {
  service_account_id = google_service_account.tf_plan.name
  role               = "roles/iam.workloadIdentityUser"
  member             = local.from_any_branch
}

# Plans run code from any branch, so this identity must not be able to read
# customer data. In the shared project (state, registry, vault metadata)
# roles/viewer is fine. In dev and prod it would include reading Firestore
# documents, so it gets only what the env stacks' resources need to be read.
# Not roles/iam.securityReviewer either: it includes datastore.entities.list.
# The IAM policies Terraform reads are covered by the roles below.
# (Deny policies would be simpler, but need a Google Cloud organization.)
locals {
  tf_plan_env_roles = [
    "roles/browser",                         # the project and its IAM policy
    "roles/run.viewer",                      # services, jobs and their IAM policies
    "roles/monitoring.viewer",               # uptime, alerts, SLOs, dashboards
    "roles/secretmanager.viewer",            # secret metadata, never values
    "roles/cloudscheduler.viewer",           # the maintenance trigger
    "roles/iam.serviceAccountViewer",        # service accounts
    "roles/serviceusage.serviceUsageViewer", # enabled APIs
  ]
  tf_plan_bindings = merge(
    { for r in ["roles/viewer", "roles/iam.securityReviewer"] : "shared/${r}" => { project = var.shared_project_id, role = r } },
    merge([for k, p in module.projects : merge(
      { for r in local.tf_plan_env_roles : "${k}/${r}" => { project = p.project_id, role = r } },
      { "${k}/firestoreMetadataReader" = { project = p.project_id, role = google_project_iam_custom_role.firestore_metadata[k].id } },
    )]...),
  )
}

# The database's settings (location, delete protection), not its documents.
resource "google_project_iam_custom_role" "firestore_metadata" {
  for_each    = module.projects
  project     = each.value.project_id
  role_id     = "firestoreMetadataReader"
  title       = "Firestore metadata reader"
  description = "Read Firestore database settings; no access to documents. For CI plans."
  permissions = ["datastore.databases.get", "datastore.databases.getMetadata", "datastore.databases.list", "datastore.locations.get", "datastore.locations.list"]
}

resource "google_project_iam_member" "tf_plan_read" {
  for_each = local.tf_plan_bindings
  project  = each.value.project
  role     = each.value.role
  member   = google_service_account.tf_plan.member
}

# Reads state; plans run with -lock=false so it never needs to write.
resource "google_storage_bucket_iam_member" "tf_plan_state" {
  bucket = google_storage_bucket.tfstate.name
  role   = "roles/storage.objectViewer"
  member = google_service_account.tf_plan.member
}

# --- Terraform: apply (main only) -----------------------------------------

resource "google_service_account" "tf_apply" {
  project      = var.shared_project_id
  account_id   = "tf-apply"
  display_name = "Terraform apply (platform + envs)"
}

resource "google_service_account_iam_member" "tf_apply_wif" {
  service_account_id = google_service_account.tf_apply.name
  role               = "roles/iam.workloadIdentityUser"
  member             = local.from_main
}

# Workload projects are fully Terraform-managed, so apply owns them...
resource "google_project_iam_member" "tf_apply_owner" {
  for_each = module.projects
  project  = each.value.project_id
  role     = "roles/owner"
  member   = google_service_account.tf_apply.member
}

# ...but in the shared project it can only manage the image registry.
resource "google_project_iam_member" "tf_apply_shared" {
  for_each = toset(["roles/artifactregistry.admin", "roles/viewer"])
  project  = var.shared_project_id
  role     = each.value
  member   = google_service_account.tf_apply.member
}

resource "google_storage_bucket_iam_member" "tf_apply_state" {
  bucket = google_storage_bucket.tfstate.name
  role   = "roles/storage.objectAdmin"
  member = google_service_account.tf_apply.member
}

# --- Image builder (main only) --------------------------------------------

resource "google_service_account" "ci_builder" {
  project      = var.shared_project_id
  account_id   = "ci-builder"
  display_name = "CI image builder"
}

resource "google_service_account_iam_member" "ci_builder_wif" {
  service_account_id = google_service_account.ci_builder.name
  role               = "roles/iam.workloadIdentityUser"
  member             = local.from_main
}

# --- Deployers, one per environment (main only) ---------------------------

resource "google_service_account" "deployer" {
  for_each     = module.projects
  project      = each.value.project_id
  account_id   = "ci-deployer"
  display_name = "CI deployer (${each.key})"
}

resource "google_service_account_iam_member" "deployer_wif" {
  for_each           = google_service_account.deployer
  service_account_id = each.value.name
  role               = "roles/iam.workloadIdentityUser"
  member             = local.from_main
}

# Read Cloud Run (to poll deploys) and Monitoring (load-test instance counts)
# across the env. Write access is granted per service in the env layer.
resource "google_project_iam_member" "deployer_read" {
  for_each = { for pair in setproduct(keys(module.projects), ["roles/run.viewer", "roles/monitoring.viewer"]) : "${pair[0]}/${pair[1]}" => pair }
  project  = module.projects[each.value[0]].project_id
  role     = each.value[1]
  member   = google_service_account.deployer[each.value[0]].member
}
