variable "project_id" {
  type = string
}

variable "region" {
  type = string
}

variable "environment" {
  description = "Shown in alert and dashboard names"
  type        = string
}

variable "service_name" {
  type = string
}

variable "service_uri" {
  type = string
}

variable "job_name" {
  description = "Maintenance job shown on the dashboard"
  type        = string
}

variable "alert_email" {
  description = "Where alerts are emailed. Empty = incidents in the console only."
  type        = string
  default     = ""
  sensitive   = true
}

variable "enable_error_rate_alert" {
  description = "5xx-ratio alert. Free until alerting billing starts (no sooner than Sept 2027), then ~$0.35/month."
  type        = bool
  default     = true
}

variable "availability_goal" {
  type    = number
  default = 0.99
}

variable "latency_threshold" {
  type    = string
  default = "1s"
}
