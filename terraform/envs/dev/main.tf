# dev environment: the hello-web app in its own project.
# Smaller ceiling and no error-rate alert: releases land here first and get smoke-tested.

terraform {
  required_version = ">= 1.11"

  backend "gcs" {
    bucket = "gcp-sandbox-78tvq4-tfstate"
    prefix = "envs/dev"
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
  env      = "dev"
  region   = data.terraform_remote_state.bootstrap.outputs.region
  project  = data.terraform_remote_state.bootstrap.outputs.environments[local.env].project_id
  deployer = data.terraform_remote_state.bootstrap.outputs.environments[local.env].deployer_email
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
  description = "Where alerts are emailed. Empty = incidents in the console only."
  type        = string
  default     = ""
}

module "app" {
  source = "../../modules/cloud-run-service"

  project_id          = local.project
  region              = local.region
  name                = "hello-web"
  deployer_email      = local.deployer
  max_instances       = 3
  admin_token_version = var.admin_token_version
}

module "pruner" {
  source = "../../modules/revision-pruner"

  project_id     = local.project
  region         = local.region
  service_name   = module.app.name
  deployer_email = local.deployer
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
  enable_error_rate_alert = false
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

output "run_maintenance_now" {
  value = "gcloud run jobs execute ${module.pruner.job_name} --project ${local.project} --region ${local.region} --wait"
}
