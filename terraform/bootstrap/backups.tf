# The backup vault: where every environment's data is copied each night.
#
# It lives here, in the shared project, so nothing in an environment - a bad
# apply, a compromised deploy identity, a deleted project - can reach it:
#
#   - each environment gets a "backup-writer" identity that can only ADD files
#     under its own prefix (objectCreator can't overwrite or delete), and read
#     them back for the restore drill
#   - a 90-day retention policy stops anyone, owners included, deleting or
#     replacing a backup until it's 90 days old
#   - CI's tf-apply has no rights on this bucket, so no pipeline can touch it
#
# Free tier: Cloud Storage gives 5 GB-month in us-central1/us-east1/us-west1.
# Nightly backups of the flight log are a few KB each.

resource "google_storage_bucket" "backups" {
  project                     = var.shared_project_id
  name                        = "${var.shared_project_id}-backups"
  location                    = upper(var.region)
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  # Unlocked on purpose: locking is permanent (it can never be shortened or
  # removed, and the bucket can't be deleted while it holds objects). For a
  # real platform, lock it once the period is agreed.
  retention_policy {
    retention_period = 90 * 24 * 60 * 60
    is_locked        = false
  }

  # Deleted objects stay recoverable for 7 days after the retention period.
  soft_delete_policy {
    retention_duration_seconds = 7 * 24 * 60 * 60
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.shared]
}

# Lives in the environment's project, so that environment's job can borrow it
# (granted in the env layer), but its only rights are on the vault, below.
resource "google_service_account" "backup_writer" {
  for_each     = module.projects
  project      = each.value.project_id
  account_id   = "backup-writer"
  display_name = "Backup writer (${each.key}): add-only access to the vault"
}

locals {
  # Matches objects under "<env>/" only, so dev can't write or read prod's backups.
  vault_prefix = { for k, _ in module.projects : k => "projects/_/buckets/${google_storage_bucket.backups.name}/objects/${k}/" }
}

resource "google_storage_bucket_iam_member" "backup_writer_create" {
  for_each = module.projects
  bucket   = google_storage_bucket.backups.name
  role     = "roles/storage.objectCreator"
  member   = google_service_account.backup_writer[each.key].member

  condition {
    title      = "${each.key}-prefix-only"
    expression = "resource.name.startsWith(\"${local.vault_prefix[each.key]}\")"
  }
}

# Read back for the restore drill and for restores. Listing is checked against
# the bucket, so the condition allows that too (it reveals names, not data).
resource "google_storage_bucket_iam_member" "backup_writer_read" {
  for_each = module.projects
  bucket   = google_storage_bucket.backups.name
  role     = "roles/storage.objectViewer"
  member   = google_service_account.backup_writer[each.key].member

  condition {
    title      = "${each.key}-prefix-and-list"
    expression = "resource.name.startsWith(\"${local.vault_prefix[each.key]}\") || resource.type == \"storage.googleapis.com/Bucket\""
  }
}
