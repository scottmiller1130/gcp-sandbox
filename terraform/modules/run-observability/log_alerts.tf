# Log-based alerts: things that should never happen silently. Each emails the
# alert channel (at most once an hour per alert) and opens an incident.
# Created only when an alert email is set: an alert nobody receives is noise.

locals {
  job_filter = "resource.type=\"cloud_run_job\" AND resource.labels.job_name=\"${var.job_name}\""

  log_alerts = {
    backup = {
      name     = "${local.label}: backup or restore drill failed"
      severity = "CRITICAL"
      filter   = "${local.job_filter} AND (jsonPayload.message=\"restore drill failed\" OR jsonPayload.message=\"backup failed\" OR jsonPayload.message=\"maintenance failed\" OR jsonPayload.message:\"backup skipped\")"
      doc      = "The nightly flight-log backup or its restore drill failed (or was skipped). Customer data written since the last good backup has **no copy in the vault**. Check the `${var.job_name}` job's last execution logs, fix, then re-run it: `gcloud run jobs execute ${var.job_name} --region ${var.region} --wait`."
    }
    admin = {
      name     = "${local.label}: failed /admin login"
      severity = "WARNING"
      filter   = "${local.run_filter} AND jsonPayload.message=\"admin: bad or missing token\""
      doc      = "Someone called `/admin` without the right token. One-offs are scanners; repeated hits from one `remote` address are a guessing attempt (the token is 48 random characters). If it looks targeted, rotate it: bump `admin_token_version` in the env's Terraform."
    }
    out_of_band = {
      name     = "${local.label}: change made outside the pipeline"
      severity = "WARNING"
      filter   = "logName:\"cloudaudit.googleapis.com%2Factivity\" AND protoPayload.authenticationInfo.principalEmail:* AND NOT protoPayload.authenticationInfo.principalEmail:\"gserviceaccount.com\""
      doc      = "A person (not CI or a service) changed something in this project directly. Expected when applying the bootstrap layer; otherwise find who and what in the audit log entry, and make sure the change is captured in a PR (the weekly drift check will flag anything Terraform manages)."
    }
  }
}

resource "google_monitoring_alert_policy" "log" {
  for_each     = local.alerting ? local.log_alerts : {}
  project      = var.project_id
  display_name = each.value.name
  combiner     = "OR"
  severity     = each.value.severity

  conditions {
    display_name = each.value.name
    condition_matched_log {
      filter = each.value.filter
    }
  }

  alert_strategy {
    notification_rate_limit {
      period = "3600s"
    }
    auto_close = "604800s"
  }

  notification_channels = local.channels
  documentation {
    mime_type = "text/markdown"
    content   = each.value.doc
  }
}
