#!/usr/bin/env bash
# Copies the bootstrap layer's outputs into GitHub repository variables, which
# the workflows read. Re-run after changing the bootstrap layer.
#   scripts/set-github-variables.sh
set -euo pipefail
cd "$(dirname "$0")/../terraform/bootstrap"
terraform output -json github_variables \
  | jq -r 'to_entries[] | "\(.key)\t\(.value)"' \
  | while IFS=$'\t' read -r key value; do
      gh variable set "$key" --body "$value"
      echo "set $key"
    done
