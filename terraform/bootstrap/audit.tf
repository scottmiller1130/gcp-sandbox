# Audit and evidence: what SOC 2 asks you to prove, kept where the pipelines
# that make changes can't edit or delete it.
#
#   deny policy    CI's plan identity can read every project (for plans) but is
#                  denied customer data, whatever roles it holds
#   audit logs     Data Access logs for secrets, storage, impersonation and the
#                  database, on top of Admin Activity (always on, 400 days)
#   archive        every audit log from all three projects copied to one log
#                  bucket in the shared project, kept 400 days
#   ledger         a write-once bucket for the change ledger: one record per
#                  apply and deploy (who, what, approved by whom)

locals {
  all_project_ids = merge({ shared = var.shared_project_id }, { for k, p in module.projects : k => p.project_id })
}

# --- Deny: the plan identity never reads customer data ---------------------

# Owner doesn't include managing deny policies, so the human who applies
# bootstrap is granted it here.
resource "google_project_iam_member" "deny_admin" {
  for_each = module.projects
  project  = each.value.project_id
  role     = "roles/iam.denyAdmin"
  member   = "user:${var.admin_email}"
}

resource "google_iam_deny_policy" "plan_reads_no_customer_data" {
  for_each     = module.projects
  parent       = urlencode("cloudresourcemanager.googleapis.com/projects/${each.value.project_id}")
  name         = "plan-reads-no-customer-data"
  display_name = "tf-plan can't read Firestore documents"

  rules {
    description = "Plans run code from any branch. roles/viewer includes datastore.entities.get/list; this takes them away."
    deny_rule {
      denied_principals  = ["principal://iam.googleapis.com/projects/-/serviceAccounts/${google_service_account.tf_plan.email}"]
      denied_permissions = ["datastore.googleapis.com/entities.get", "datastore.googleapis.com/entities.list"]
    }
  }

  depends_on = [google_project_iam_member.deny_admin]
}

# --- Data Access audit logs -------------------------------------------------

locals {
  # project -> service -> log types. Admin Activity is always on and free.
  audit = {
    shared = {
      "storage.googleapis.com"        = ["DATA_READ", "DATA_WRITE"] # state, vault, ledger buckets
      "iamcredentials.googleapis.com" = ["DATA_READ"]               # every CI token exchange
      "sts.googleapis.com"            = ["DATA_READ"]
    }
    env = {
      "secretmanager.googleapis.com"  = ["DATA_READ"]                # every secret access
      "iamcredentials.googleapis.com" = ["DATA_READ"]                # the backup job borrowing the vault writer
      "datastore.googleapis.com"      = ["ADMIN_READ", "DATA_WRITE"] # database settings read, every write
    }
  }
  audit_configs = merge(
    { for svc, types in local.audit.shared : "shared/${svc}" => { project = var.shared_project_id, service = svc, types = types } },
    merge([for k, p in module.projects : { for svc, types in local.audit.env : "${k}/${svc}" => { project = p.project_id, service = svc, types = types } }]...),
  )
}

resource "google_project_iam_audit_config" "data_access" {
  for_each = local.audit_configs
  project  = each.value.project
  service  = each.value.service

  dynamic "audit_log_config" {
    for_each = each.value.types
    content {
      log_type = audit_log_config.value
    }
  }
}

# --- Archive: all audit logs, all projects, 400 days ------------------------

# Beyond 30 days Cloud Logging bills ~$0.01/GiB-month; audit logs here are
# a few MB a month, so about a cent. The cost review flags it for sign-off.
resource "google_logging_project_bucket_config" "audit" {
  project        = var.shared_project_id
  location       = "global"
  bucket_id      = "audit-archive"
  retention_days = 400
  description    = "Audit logs (Admin Activity, Data Access, System Event, Policy Denied) from every sandbox project"
  # Unlocked: locking is permanent. Lock it once the retention period is agreed.
  locked = false
}

resource "google_logging_project_sink" "audit" {
  for_each               = local.all_project_ids
  project                = each.value
  name                   = "audit-to-archive"
  destination            = "logging.googleapis.com/${google_logging_project_bucket_config.audit.id}"
  filter                 = "logName:\"cloudaudit.googleapis.com\""
  unique_writer_identity = true
}

resource "google_project_iam_member" "audit_sink_writer" {
  for_each = google_logging_project_sink.audit
  project  = var.shared_project_id
  role     = "roles/logging.bucketWriter"
  member   = each.value.writer_identity

  condition {
    title      = "audit-archive-only"
    expression = "resource.name.endsWith(\"/buckets/${google_logging_project_bucket_config.audit.bucket_id}\")"
  }
}

# --- Change ledger ----------------------------------------------------------

resource "google_storage_bucket" "ledger" {
  # checkov:skip=CKV_GCP_78:Versioning adds nothing here: the retention policy already stops any object being overwritten or deleted
  # checkov:skip=CKV_GCP_62:Data Access audit logs on storage.googleapis.com record every read and write, with identity (stronger than access logs)
  project                     = var.shared_project_id
  name                        = "${var.shared_project_id}-ledger"
  location                    = upper(var.region)
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  retention_policy {
    retention_period = 400 * 24 * 60 * 60
    is_locked        = false # lock once agreed: it's permanent
  }

  soft_delete_policy {
    retention_duration_seconds = 7 * 24 * 60 * 60
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.shared]
}

# The identities that make changes may add records (and read them, to chain
# each record to the previous one), never change or delete them.
locals {
  ledger_writers = merge(
    { "tf-apply" = google_service_account.tf_apply.member },
    { for k, sa in google_service_account.deployer : "deployer-${k}" => sa.member },
  )
  ledger_roles = { create = "roles/storage.objectCreator", read = "roles/storage.objectViewer" }
}

resource "google_storage_bucket_iam_member" "ledger" {
  for_each = { for pair in setproduct(keys(local.ledger_writers), keys(local.ledger_roles)) : "${pair[0]}/${pair[1]}" => pair }
  bucket   = google_storage_bucket.ledger.name
  role     = local.ledger_roles[each.value[1]]
  member   = local.ledger_writers[each.value[0]]
}
