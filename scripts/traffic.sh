#!/usr/bin/env bash
# Prints the current traffic split: percent, revision, tag.
#   traffic.sh SERVICE REGION
set -euo pipefail
gcloud run services describe "$1" --region "$2" --format json \
  | jq -r '.status.traffic[] | "\(.percent // 0)%\t\(.revisionName)\t\(.tag // "")"' \
  | sort -rn | column -t -s $'\t'
