# Read by the platform and env layers through terraform_remote_state.

output "region" {
  value = var.region
}

output "shared_project_id" {
  value = var.shared_project_id
}

output "state_bucket" {
  value = google_storage_bucket.tfstate.name
}

output "workload_identity_provider" {
  value = google_iam_workload_identity_pool_provider.github.name
}

output "ledger_bucket" {
  value = google_storage_bucket.ledger.name
}

output "audit_archive" {
  description = "Query in Logs Explorer: scope to this log bucket"
  value       = google_logging_project_bucket_config.audit.id
}

output "backup_bucket" {
  value = google_storage_bucket.backups.name
}

output "ci_builder_email" {
  value = google_service_account.ci_builder.email
}

output "environments" {
  value = {
    for k, p in module.projects : k => {
      project_id        = p.project_id
      project_number    = p.number
      deployer_email    = google_service_account.deployer[k].email
      run_service_agent = p.service_agents["run.googleapis.com"]
      backup_writer     = google_service_account.backup_writer[k].email
      backup_prefix     = "${k}/"
    }
  }
}

output "github_variables" {
  description = "Repository variables the workflows read (see docs/terraform.md for the gh command)"
  value = {
    GCP_REGION       = var.region
    GCP_WIF_PROVIDER = google_iam_workload_identity_pool_provider.github.name
    GCP_AR_REPO      = "${var.region}-docker.pkg.dev/${var.shared_project_id}/apps"
    CI_BUILDER_SA    = google_service_account.ci_builder.email
    TF_PLAN_SA       = google_service_account.tf_plan.email
    TF_APPLY_SA      = google_service_account.tf_apply.email
    DEV_PROJECT_ID   = module.projects["dev"].project_id
    DEV_DEPLOYER_SA  = google_service_account.deployer["dev"].email
    PROD_PROJECT_ID  = module.projects["prod"].project_id
    PROD_DEPLOYER_SA = google_service_account.deployer["prod"].email
  }
}
