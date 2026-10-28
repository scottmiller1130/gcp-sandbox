# prod environment: the hello-web app in its own project.
# Canary releases land here, gated on the new revision's error rate.

terraform {
  required_version = ">= 1.11"

  backend "gcs" {
    bucket = "gcp-sandbox-78tvq4-tfstate"
    prefix = "envs/prod"
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

data "terraform_remote_state" "bootstrap" {
  backend = "gcs"
  config = {
    bucket = "gcp-sandbox-78tvq4-tfstate"
    prefix = "bootstrap"
  }
}

locals {
  env      = "prod"
  region   = data.terraform_remote_state.bootstrap.outputs.region
  project  = data.terraform_remote_state.bootstrap.outputs.environments[local.env].project_id
  deployer = data.terraform_remote_state.bootstrap.outputs.environments[local.env].deployer_email
  backups  = data.terraform_remote_state.bootstrap.outputs.environments[local.env]
  vault    = data.terraform_remote_state.bootstrap.outputs.backup_bucket
}

provider "google" {
  project = local.project
  region  = local.region

  default_labels = {
    env     = local.env
    managed = "terraform"
    layer   = "envs"
  }
}

variable "admin_token_version" {
  description = "Bump to rotate the /admin token"
  type        = number
  default     = 1
}

variable "alert_email" {
  description = "Where alerts are emailed. Set by CI from the ALERT_EMAIL secret (TF_VAR_alert_email); empty = incidents in the console only, and no log-based alerts."
  type        = string
  default     = ""
  sensitive   = true
}

module "app" {
  source = "../../modules/cloud-run-service"

  project_id          = local.project
  region              = local.region
  name                = "hello-web"
  deployer_email      = local.deployer
  max_instances       = 10
  admin_token_version = var.admin_token_version
}

module "pruner" {
  source = "../../modules/revision-pruner"

  project_id     = local.project
  region         = local.region
  service_name   = module.app.name
  deployer_email = local.deployer
  backup_bucket  = local.vault
  backup_prefix  = local.backups.backup_prefix
  backup_writer  = local.backups.backup_writer
}

module "data" {
  source = "../../modules/firestore"

  project_id          = local.project
  app_service_account = module.app.runtime_service_account
  job_service_account = module.pruner.service_account
  backup_writer       = local.backups.backup_writer
}

module "observability" {
  source = "../../modules/run-observability"

  project_id              = local.project
  region                  = local.region
  environment             = local.env
  service_name            = module.app.name
  service_uri             = module.app.uri
  job_name                = module.pruner.job_name
  alert_email             = var.alert_email
  enable_error_rate_alert = true
}

output "service_url" {
  value = module.app.uri
}

output "dashboard_url" {
  value = module.observability.dashboard_url
}

output "admin_curl" {
  description = "Call /admin with the token from Secret Manager"
  value       = "curl -s -H \"Authorization: Bearer $(gcloud secrets versions access latest --project ${local.project} --secret ${module.app.admin_token_secret})\" ${module.app.uri}/admin | jq"
}

output "restore_from_vault" {
  description = "Replay every backup into Firestore (idempotent). Drop DRY_RUN=true to write."
  value       = "gcloud run jobs execute ${module.pruner.job_name} --project ${local.project} --region ${local.region} --args restore --update-env-vars DRY_RUN=true --wait"
}

output "run_maintenance_now" {
  value = "gcloud run jobs execute ${module.pruner.job_name} --project ${local.project} --region ${local.region} --wait"
}
