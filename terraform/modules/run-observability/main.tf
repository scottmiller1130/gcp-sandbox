# Uptime check, alerts, SLOs and a dashboard for one Cloud Run service.
#
# Cost: uptime checks are free up to 1M runs/month per project (this one: ~26k).
# Alerting is free until at least Sept 2027. After that the uptime alert stays
# free and the error-rate alert would cost ~$0.35/month. SLOs, dashboards,
# Cloud Trace and Error Reporting are free at this scale.

terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }
}

locals {
  host        = trimprefix(var.service_uri, "https://")
  run_filter  = "resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"${var.service_name}\""
  by_revision = ["resource.label.revision_name"]
  # The address is sensitive; whether one is set isn't.
  alerting = nonsensitive(var.alert_email != "")
  channels = local.alerting ? [google_monitoring_notification_channel.email[0].id] : []
  label    = "${var.service_name} (${var.environment})"
}

resource "google_monitoring_notification_channel" "email" {
  count        = local.alerting ? 1 : 0
  project      = var.project_id
  display_name = "${var.environment} alerts"
  type         = "email"
  labels = {
    email_address = var.alert_email
  }
}

# --- Uptime ---------------------------------------------------------------

resource "google_monitoring_uptime_check_config" "this" {
  project      = var.project_id
  display_name = "${local.label} homepage"
  timeout      = "10s"
  period       = "300s"

  http_check {
    path         = "/"
    port         = 443
    use_ssl      = true
    validate_ssl = true
    accepted_response_status_codes {
      status_class = "STATUS_CLASS_2XX"
    }
  }

  monitored_resource {
    type = "uptime_url"
    labels = {
      project_id = var.project_id
      host       = local.host
    }
  }

  # Three US probe locations (the minimum).
  selected_regions = ["USA_OREGON", "USA_IOWA", "USA_VIRGINIA"]
}

resource "google_monitoring_alert_policy" "uptime" {
  project      = var.project_id
  display_name = "${local.label} is down"
  combiner     = "OR"

  conditions {
    display_name = "Homepage failing from 2+ locations"
    condition_threshold {
      filter          = "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\" AND metric.label.check_id=\"${google_monitoring_uptime_check_config.this.uptime_check_id}\""
      comparison      = "COMPARISON_GT"
      threshold_value = 1
      duration        = "60s"
      aggregations {
        alignment_period     = "1200s"
        per_series_aligner   = "ALIGN_NEXT_OLDER"
        cross_series_reducer = "REDUCE_COUNT_FALSE"
        group_by_fields      = ["resource.label.*"]
      }
      trigger {
        count = 1
      }
    }
  }

  notification_channels = local.channels
  documentation {
    mime_type = "text/markdown"
    content   = "The ${local.label} homepage is failing uptime checks. Check the dashboard and recent deploys; run the **Rollback** workflow if a release caused it."
  }
}

# --- Error rate -----------------------------------------------------------

resource "google_monitoring_alert_policy" "errors" {
  count        = var.enable_error_rate_alert ? 1 : 0
  project      = var.project_id
  display_name = "${local.label} 5xx rate above 5%"
  combiner     = "OR"

  conditions {
    display_name = "5xx / all requests > 5% over 5 min"
    condition_threshold {
      filter             = "${local.run_filter} AND metric.type=\"run.googleapis.com/request_count\" AND metric.labels.response_code_class=\"5xx\""
      denominator_filter = "${local.run_filter} AND metric.type=\"run.googleapis.com/request_count\""
      comparison         = "COMPARISON_GT"
      threshold_value    = 0.05
      duration           = "0s"
      aggregations {
        alignment_period     = "300s"
        per_series_aligner   = "ALIGN_DELTA"
        cross_series_reducer = "REDUCE_SUM"
      }
      denominator_aggregations {
        alignment_period     = "300s"
        per_series_aligner   = "ALIGN_DELTA"
        cross_series_reducer = "REDUCE_SUM"
      }
    }
  }

  notification_channels = local.channels
  documentation {
    mime_type = "text/markdown"
    content   = "More than 5% of ${local.label} requests are failing. Expected while practising with the Deploy workflow's `error_rate` input; otherwise check Error Reporting and consider **Rollback**."
  }
}

# --- SLOs -----------------------------------------------------------------

resource "google_monitoring_service" "this" {
  project      = var.project_id
  service_id   = var.service_name
  display_name = local.label

  basic_service {
    service_type = "CLOUD_RUN"
    service_labels = {
      service_name = var.service_name
      location     = var.region
    }
  }
}

resource "google_monitoring_slo" "availability" {
  project             = var.project_id
  service             = google_monitoring_service.this.service_id
  slo_id              = "availability"
  display_name        = "${var.availability_goal * 100}% of requests succeed (7-day rolling)"
  goal                = var.availability_goal
  rolling_period_days = 7

  basic_sli {
    availability {
      enabled = true
    }
  }
}

resource "google_monitoring_slo" "latency" {
  project             = var.project_id
  service             = google_monitoring_service.this.service_id
  slo_id              = "latency"
  display_name        = "95% of requests under ${var.latency_threshold} (7-day rolling)"
  goal                = 0.95
  rolling_period_days = 7

  basic_sli {
    latency {
      threshold = var.latency_threshold
    }
  }
}

# --- Dashboard ------------------------------------------------------------

locals {
  charts = {
    requests = {
      title   = "Requests/s by revision (watch canaries shift here)"
      filter  = "${local.run_filter} AND metric.type=\"run.googleapis.com/request_count\""
      aligner = "ALIGN_RATE", reducer = "REDUCE_SUM", group = local.by_revision, plot = "STACKED_AREA"
    }
    errors = {
      title   = "5xx/s by revision"
      filter  = "${local.run_filter} AND metric.type=\"run.googleapis.com/request_count\" AND metric.labels.response_code_class=\"5xx\""
      aligner = "ALIGN_RATE", reducer = "REDUCE_SUM", group = local.by_revision, plot = "LINE"
    }
    latency = {
      title   = "p95 latency by revision (ms)"
      filter  = "${local.run_filter} AND metric.type=\"run.googleapis.com/request_latencies\""
      aligner = "ALIGN_DELTA", reducer = "REDUCE_PERCENTILE_95", group = local.by_revision, plot = "LINE"
    }
    instances = {
      title   = "Instances (active vs idle)"
      filter  = "${local.run_filter} AND metric.type=\"run.googleapis.com/container/instance_count\""
      aligner = "ALIGN_MAX", reducer = "REDUCE_SUM", group = ["metric.label.state"], plot = "STACKED_BAR"
    }
    uptime = {
      title   = "Uptime check pass rate"
      filter  = "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\" AND metric.label.check_id=\"${google_monitoring_uptime_check_config.this.uptime_check_id}\""
      aligner = "ALIGN_FRACTION_TRUE", reducer = "REDUCE_MEAN", group = [], plot = "LINE"
    }
    maintenance = {
      title   = "Maintenance job executions"
      filter  = "resource.type=\"cloud_run_job\" AND resource.labels.job_name=\"${var.job_name}\" AND metric.type=\"run.googleapis.com/job/completed_execution_count\""
      aligner = "ALIGN_DELTA", reducer = "REDUCE_SUM", group = ["metric.label.result"], plot = "STACKED_BAR"
    }
  }

  # [chart, x, y] on a 48-column grid
  layout = [
    ["requests", 0, 0], ["errors", 24, 0],
    ["latency", 0, 16], ["instances", 24, 16],
    ["uptime", 0, 32], ["maintenance", 24, 32],
  ]
}

# The API drops zero and empty values (xPos = 0, groupByFields = []) from what it
# stores, so they're left out here too - otherwise every plan shows a diff.
resource "google_monitoring_dashboard" "this" {
  project = var.project_id
  dashboard_json = jsonencode({
    displayName = local.label
    mosaicLayout = {
      columns = 48
      tiles = [for t in local.layout : merge(
        t[1] == 0 ? {} : { xPos = t[1] },
        t[2] == 0 ? {} : { yPos = t[2] },
        {
          width  = 24
          height = 16
          widget = {
            title = local.charts[t[0]].title
            xyChart = {
              chartOptions = { mode = "COLOR" }
              dataSets = [{
                plotType   = local.charts[t[0]].plot
                targetAxis = "Y1"
                timeSeriesQuery = {
                  timeSeriesFilter = {
                    filter = local.charts[t[0]].filter
                    aggregation = merge(
                      {
                        alignmentPeriod    = "60s"
                        perSeriesAligner   = local.charts[t[0]].aligner
                        crossSeriesReducer = local.charts[t[0]].reducer
                      },
                      length(local.charts[t[0]].group) == 0 ? {} : { groupByFields = local.charts[t[0]].group },
                    )
                  }
                }
              }]
            }
          }
      })]
    }
  })
}
