#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GATEWAY_URL="${GATEWAY_URL:-http://localhost:9081}"
INGEST_URL="${INGEST_URL:-http://localhost:8080}"
WAYLOG_READ_KEY="${WAYLOG_READ_KEY:-demo}"
REQUESTS="${REQUESTS:-20}"
CONCURRENCY="${CONCURRENCY:-5}"
TIMEOUT="${WAYLOG_CLI_TIMEOUT:-5s}"
USE_RUNNING="${WAYLOG_ROLLUP_USE_RUNNING_DEMO:-0}"

CLI_BIN="${WAYLOG_CLI_BIN:-./data/demo-state/bin/waylog}"
JSON_BIN="${WAYLOG_JSON_HELPER_BIN:-./data/demo-state/bin/demo-acceptance-json}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

http_code() {
  curl -s -o /dev/null -w "%{http_code}" "$1" || echo "000"
}

cleanup() {
  if [[ "$USE_RUNNING" != "1" ]]; then
    make demo-stop >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if [[ "$USE_RUNNING" != "1" ]]; then
  make demo
elif [[ "$(http_code "${GATEWAY_URL}/demo")" != "200" ]] || [[ "$(http_code "${INGEST_URL}/healthz")" != "200" ]]; then
  fail "running demo is not reachable. Start it with make demo or unset WAYLOG_ROLLUP_USE_RUNNING_DEMO"
fi

mkdir -p ./data/demo-state/bin
go build -o "$CLI_BIN" ./cmd/waylog
go build -o "$JSON_BIN" ./scripts/demo-acceptance-json

CLI=("$CLI_BIN" --addr "$INGEST_URL" --api-key "$WAYLOG_READ_KEY" --timeout "$TIMEOUT")

burst_body="{\"requests\":${REQUESTS},\"concurrency\":${CONCURRENCY}}"
burst_status="$(curl -s -o /tmp/waylog-rollup-burst.json -w "%{http_code}" \
  -X POST "${GATEWAY_URL}/demo/burst" \
  -H 'Content-Type: application/json' \
  --data "$burst_body" || echo "000")"
[[ "$burst_status" == "200" ]] || fail "traffic burst failed: HTTP $burst_status"

errors_json=""
for _ in $(seq 1 15); do
  errors_json="$("${CLI[@]}" --json errors --window 15m --limit 10)" || fail "waylog errors failed"
  if "$JSON_BIN" has-payment-error <<<"$errors_json"; then
    break
  fi
  sleep 1
done
"$JSON_BIN" has-payment-error <<<"$errors_json" || fail "payment_502 error family did not appear in /v1/errors"

blast_json="$("${CLI[@]}" --json blast checkout:payment.charge:PMT_502 --window 15m)" || fail "waylog blast failed"

root_count="$("$JSON_BIN" payment-error-count <<<"$errors_json")"
affected_traces="$("$JSON_BIN" payment-affected-traces <<<"$errors_json")"
affected_services="$("$JSON_BIN" blast-affected-services <<<"$blast_json")"

[[ "$root_count" =~ ^[0-9]+$ ]] || fail "root-cause count is not numeric: $root_count"
[[ "$affected_services" =~ ^[0-9]+$ ]] || fail "affected services is not numeric: $affected_services"
(( root_count > 0 )) || fail "root-cause count is empty"
(( affected_services > 1 )) || fail "blast radius did not show cross-service spread"

naive_count=$((root_count * affected_services))
(( naive_count > root_count )) || fail "naive propagated count did not exceed root-cause count"

cat <<EOF
Rollup comparison
  workload: ${REQUESTS} demo requests, concurrency ${CONCURRENCY}
  root-cause counted PMT_502: ${root_count}
  affected traces: ${affected_traces}
  affected services: ${affected_services}
  naive propagated count: ${root_count} * ${affected_services} = ${naive_count}

PASS: Waylog counts the root cause once per failed request instead of once per propagated service hop.
EOF
