#!/usr/bin/env bash
# demo-deploy-failure.sh — Scenario: deploy webhook → payment 502s → dashboard
set -euo pipefail
source "$(dirname "$0")/_lib.sh"

send_event() {
  local trace_id="$1"
  local span_id="$2"
  local service="$3"
  local success="$4"
  local status_code="$5"
  local latency_ms="$6"
  local deployment_id="${7:-}"
  local error_code="${8:-}"
  local error_message="${9:-}"
  local version="${10:-v2.0.0}"

  local event_name="${service}.request"
  if [[ "$success" == "false" ]]; then
    event_name="${service}.error"
  fi

  local ts
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

  local error_block=""
  if [[ -n "$error_code" ]]; then
    error_block=$(printf ',"error":{"code":"%s","message":"%s"}' "$error_code" "$error_message")
  fi

  local body
  body=$(printf '{
    "schema_version":"1.0",
    "event_name":"%s",
    "timestamp":"%s",
    "system":{"service":"%s","env":"prod","version":"%s","deployment_id":"%s"},
    "request":{"trace_id":"%s","span_id":"%s","flow":"purchase"},
    "user":{"id":"user_%s","tier":"premium","region":"us-east-1"},
    "outcome":{"status_code":%d,"success":%s},
    "metrics":{"latency_ms":%d}%s
  }' "$event_name" "$ts" "$service" "$version" "$deployment_id" "$trace_id" "$span_id" \
     "$(rand_hex 4)" "$status_code" "$success" "$latency_ms" "$error_block")

  curl -s -o /dev/null -w "" -X POST -H "Content-Type: application/json" -d "$body" "${BASE_URL}/v1/events"
}

# -------------------------------------------------------------------
echo "=== Deploy-Failure Demo ==="
echo ""
echo "Target: ${BASE_URL}"
echo ""

# Step 0: Wait for stack ready
echo "--- Waiting for stack readiness ---"
wait_ready
echo ""

# Step 1: Send 60 healthy baseline events for payment-service
echo "--- Step 1: Sending 60 healthy baseline events (payment-service v2.0.0) ---"
for i in $(seq 1 60); do
  trace_id=$(rand_hex 32)
  span_id=$(rand_hex 16)
  latency=$((50 + RANDOM % 100))
  send_event "$trace_id" "$span_id" "payment-service" "true" "200" "$latency" "" "" ""
done
echo "Sent 60 healthy events."
echo ""

# Brief pause so events settle before the deployment marker
sleep 2

# Step 2: Register deployment v2.1
echo "--- Step 2: Registering deployment deploy_v2.1 ---"
deploy_body='{"id":"deploy_v2.1","service":"payment-service","env":"prod","version":"v2.1.0"}'
check_post "POST /v1/deployments (register deploy_v2.1)" "${BASE_URL}/v1/deployments" "$deploy_body" "201"
echo ""

# Brief pause so deployment timestamp separates before/after windows
sleep 2

# Step 3: Send 40 failure events with deployment_id
echo "--- Step 3: Sending 40 failure events (payment-service v2.1.0, PMT_502) ---"
for i in $(seq 1 40); do
  trace_id=$(rand_hex 32)
  span_id=$(rand_hex 16)
  latency=$((200 + RANDOM % 300))
  send_event "$trace_id" "$span_id" "payment-service" "false" "502" "$latency" \
    "deploy_v2.1" "PMT_502" "upstream payment gateway timeout" "v2.1.0"
done
echo "Sent 40 failure events."
echo ""

# Allow time for ingestion and graph building
sleep 3

# Step 4: Verify endpoints
echo "--- Step 4: Verifying API endpoints ---"
check "GET /v1/overview"           "${BASE_URL}/v1/overview?window=10m"          "200"
check "GET /v1/traces/recent"      "${BASE_URL}/v1/traces/recent?limit=10"      "200"
check "GET /v1/deployments"        "${BASE_URL}/v1/deployments?window=10m"      "200"
echo ""

# Step 5: Print key responses
echo "--- API Response Snapshots ---"
echo ""

echo ">> Overview (window=10m):"
curl -s "${BASE_URL}/v1/overview?window=10m" | pretty_json
echo ""

echo ">> Recent Traces (limit=5, failures_only):"
curl -s "${BASE_URL}/v1/traces/recent?limit=5&failures_only=true" | pretty_json
echo ""

echo ">> Deployments (window=10m):"
curl -s "${BASE_URL}/v1/deployments?window=10m" | pretty_json
echo ""

# Step 6: Print dashboard URL
echo "--- Dashboard ---"
echo "Open: ${BASE_URL}/ui/"
echo ""

# Results
print_results
