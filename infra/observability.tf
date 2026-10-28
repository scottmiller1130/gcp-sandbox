# Uptime check, alerts, SLOs and a dashboard for the service.
#
# Cost: uptime checks are free up to 1M runs/month (this one: ~26k). Alerting
# is free until at least Sept 2027. After that the uptime alert stays free
# and the error-rate alert would cost ~$0.35/month (set enable_error_rate_alert
# = false to avoid it). SLOs, dashboards, Cloud Trace (2.5M spans) and Error
# Reporting are free at this scale.

locals {
  service_host = trimprefix(google_cloud_run_v2_service.app.uri, "https://")
  run_filter   = "resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"${var.service_name}\""
  by_revision  = ["resource.label.revision_name"]
  channels     = var.alert_email == "" ? [] : [google_monitoring_notification_channel.email[0].id]
}

resource "google_monitoring_notification_channel" "email" {
  count        = var.alert_email == "" ? 0 : 1
  display_name = "Sandbox alerts"
  type         = "email"
  labels = {
    email_address = var.alert_email
  }
}

# --- Uptime ---------------------------------------------------------------

resource "google_monitoring_uptime_check_config" "app" {
  display_name = "${var.service_name} homepage"
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
      host       = local.service_host
    }
  }

  # Three US probe locations (the minimum).
  selected_regions = ["USA_OREGON", "USA_IOWA", "USA_VIRGINIA"]

  depends_on = [google_project_service.enabled]
}

resource "google_monitoring_alert_policy" "uptime" {
  display_name = "${var.service_name} is down"
  combiner     = "OR"

  conditions {
    display_name = "Homepage failing from 2+ locations"
    condition_threshold {
      filter          = "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\" AND metric.label.check_id=\"${google_monitoring_uptime_check_config.app.uptime_check_id}\""
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
    content   = "The ${var.service_name} homepage is failing uptime checks. Check the dashboard and recent deploys; run the **Rollback** workflow if a release caused it."
  }
}

# --- Error rate -----------------------------------------------------------

resource "google_monitoring_alert_policy" "errors" {
  count        = var.enable_error_rate_alert ? 1 : 0
  display_name = "${var.service_name} 5xx rate above 5%"
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
    content   = "More than 5% of ${var.service_name} requests are failing. Expected while practising with the Deploy workflow's `error_rate` input; otherwise check Error Reporting and consider **Rollback**."
  }
}

# --- SLOs -----------------------------------------------------------------

resource "google_monitoring_service" "app" {
  service_id   = var.service_name
  display_name = "${var.service_name} (Cloud Run)"

  basic_service {
    service_type = "CLOUD_RUN"
    service_labels = {
      service_name = var.service_name
      location     = var.region
    }
  }

  depends_on = [google_cloud_run_v2_service.app]
}

resource "google_monitoring_slo" "availability" {
  service             = google_monitoring_service.app.service_id
  slo_id              = "availability"
  display_name        = "99% of requests succeed (7-day rolling)"
  goal                = 0.99
  rolling_period_days = 7

  basic_sli {
    availability {
      enabled = true
    }
  }
}

resource "google_monitoring_slo" "latency" {
  service             = google_monitoring_service.app.service_id
  slo_id              = "latency"
  display_name        = "95% of requests under 1s (7-day rolling)"
  goal                = 0.95
  rolling_period_days = 7

  basic_sli {
    latency {
      threshold = "1s"
    }
  }
}

# --- Dashboard ------------------------------------------------------------

locals {
  # One time-series line chart, grouped and aggregated as given.
  chart = {
    for k, v in {
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
        filter  = "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\" AND metric.label.check_id=\"${google_monitoring_uptime_check_config.app.uptime_check_id}\""
        aligner = "ALIGN_FRACTION_TRUE", reducer = "REDUCE_MEAN", group = [], plot = "LINE"
      }
      maintenance = {
        title   = "Maintenance job executions"
        filter  = "resource.type=\"cloud_run_job\" AND resource.labels.job_name=\"${google_cloud_run_v2_job.maintenance.name}\" AND metric.type=\"run.googleapis.com/job/completed_execution_count\""
        aligner = "ALIGN_DELTA", reducer = "REDUCE_SUM", group = ["metric.label.result"], plot = "STACKED_BAR"
      }
      } : k => {
      title = v.title
      xyChart = {
        chartOptions = { mode = "COLOR" }
        dataSets = [{
          plotType   = v.plot
          targetAxis = "Y1"
          timeSeriesQuery = {
            timeSeriesFilter = {
              filter = v.filter
              aggregation = {
                alignmentPeriod    = "60s"
                perSeriesAligner   = v.aligner
                crossSeriesReducer = v.reducer
                groupByFields      = v.group
              }
            }
          }
        }]
      }
    }
  }

  layout = [
    ["requests", 0, 0], ["errors", 24, 0],
    ["latency", 0, 16], ["instances", 24, 16],
    ["uptime", 0, 32], ["maintenance", 24, 32],
  ]
}

resource "google_monitoring_dashboard" "app" {
  dashboard_json = jsonencode({
    displayName = var.service_name
    mosaicLayout = {
      columns = 48
      tiles = [for t in local.layout : {
        xPos   = t[1]
        yPos   = t[2]
        width  = 24
        height = 16
        widget = local.chart[t[0]]
      }]
    }
  })
}
