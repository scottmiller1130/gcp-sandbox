#!/usr/bin/env bash
# Sends requests to URL/api/info and checks the error rate.
#
#   verify.sh URL REQUESTS MAX_ERROR_PCT [REVISION]
#
# Without REVISION: fails if more than MAX_ERROR_PCT% of all requests fail.
# With REVISION:    only counts responses from that revision (error responses
#                   name their revision too), and reports its share of traffic.
#                   This is the canary check: is the *new* revision healthy?
set -euo pipefail

url=$1 n=$2 max_pct=$3 rev=${4:-}
total=0 errors=0 seen=0 seen_errors=0

for _ in $(seq "$n"); do
  resp=$(curl -s --max-time 5 -w '\n%{http_code}' "$url/api/info" || printf '\n000')
  code=${resp##*$'\n'}
  served_by=$(jq -r '.revision // empty' <<<"${resp%$'\n'*}" 2>/dev/null || true)
  total=$((total + 1))
  [ "$code" = 200 ] || errors=$((errors + 1))
  if [ -n "$rev" ] && [ "$served_by" = "$rev" ]; then
    seen=$((seen + 1))
    [ "$code" = 200 ] || seen_errors=$((seen_errors + 1))
  fi
done

echo "all revisions: $total requests, $errors errors ($((errors * 100 / total))%)"
if [ -z "$rev" ]; then
  [ $((errors * 100)) -le $((max_pct * total)) ] || { echo "FAIL: error rate above ${max_pct}%"; exit 1; }
  exit 0
fi

echo "$rev: served $seen ($((seen * 100 / total))% of traffic), $seen_errors errors"
if [ "$seen" -eq 0 ]; then
  echo "FAIL: $rev served no requests"
  exit 1
fi
[ $((seen_errors * 100)) -le $((max_pct * seen)) ] || { echo "FAIL: $rev error rate above ${max_pct}%"; exit 1; }
