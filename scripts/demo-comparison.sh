#!/usr/bin/env bash
# demo-comparison.sh — Scenario: compare_windows baseline vs current
set -euo pipefail
source "$(dirname "$0")/_lib.sh"

echo "=== compare_windows Demo: Before/After Error Analysis ==="
echo ""

# ── Step 1: Wait for stack ready ──────────────────────────────────
wait_ready

# ── Step 2: Send 40 healthy baseline events ───────────────────────
echo ""
echo "→ Sending 40 healthy baseline events for api-gateway ..."
BASELINE_TRACE_PREFIX="aaaa0000bbbb0000cccc0000dddd"
for i in $(seq 1 40); do
  idx=$(printf "%04d" "$i")
  trace_id="${BASELINE_TRACE_PREFIX}${idx}"
  span_id="1111000022220${idx}"
  ts=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")

  curl -s -o /dev/null -w "" "${BASE_URL}/v1/events" \
    -H "Content-Type: application/json" \
    -d "{
      \"schema_version\": \"1.0\",
      \"event_name\": \"api-gateway.request\",
      \"timestamp\": \"${ts}\",
      \"system\": {\"service\": \"api-gateway\", \"env\": \"demo\"},
      \"request\": {\"trace_id\": \"${trace_id}\", \"span_id\": \"${span_id}\", \"flow\": \"purchase\"},
      \"user\": {\"id\": \"user-baseline-${idx}\", \"tier\": \"free\", \"region\": \"us-east-1\"},
      \"outcome\": {\"status_code\": 200, \"success\": true},
      \"metrics\": {\"latency_ms\": $((50 + RANDOM % 100))}
    }"
done
echo "  Sent 40 healthy events (status_code=200, success=true)."

# ── Step 3: Time separation ───────────────────────────────────────
echo ""
echo "→ Sleeping 5s for time separation between windows ..."
sleep 5

# ── Step 4: Send 20 failure events with NEW_ERROR_CODE ────────────
echo ""
echo "→ Sending 20 failure events with error_code=NEW_ERROR_CODE ..."
FAILURE_TRACE_PREFIX="ffff1111eeee2222dddd3333cccc"
for i in $(seq 1 20); do
  idx=$(printf "%04d" "$i")
  trace_id="${FAILURE_TRACE_PREFIX}${idx}"
  span_id="aaaa3333bbbb4${idx}"
  ts=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")

  curl -s -o /dev/null -w "" "${BASE_URL}/v1/events" \
    -H "Content-Type: application/json" \
    -d "{
      \"schema_version\": \"1.0\",
      \"event_name\": \"api-gateway.error\",
      \"timestamp\": \"${ts}\",
      \"system\": {\"service\": \"api-gateway\", \"env\": \"demo\"},
      \"request\": {\"trace_id\": \"${trace_id}\", \"span_id\": \"${span_id}\", \"flow\": \"purchase\"},
      \"user\": {\"id\": \"user-fail-${idx}\", \"tier\": \"paid\", \"region\": \"us-east-1\"},
      \"outcome\": {\"status_code\": 502, \"success\": false},
      \"metrics\": {\"latency_ms\": $((500 + RANDOM % 300))},
      \"error\": {\"code\": \"NEW_ERROR_CODE\", \"message\": \"upstream dependency timed out\"}
    }"
done
echo "  Sent 20 failure events (error_code=NEW_ERROR_CODE, status_code=502)."

# Allow graph to settle
sleep 2

# ── Step 5: Verify tool list endpoint ─────────────────────────────
echo ""
echo "→ Verifying tool list endpoint ..."
check "GET /v1/tools" "${BASE_URL}/v1/tools" "200"

# ── Step 6: Call compare_windows tool ─────────────────────────────
echo ""
echo "→ Calling compare_windows: current=2m, baseline=2m, offset=10s ..."
compare_resp=$(curl -s -w "\n%{http_code}" "${BASE_URL}/v1/tools/compare_windows" \
  -H "Content-Type: application/json" \
  -d '{"current":"2m","baseline":"2m","offset":"10s"}')

compare_body=$(echo "$compare_resp" | sed '$d')
compare_status=$(echo "$compare_resp" | tail -1)

check_status "POST /v1/tools/compare_windows returns 200" "$compare_status" "200"

# ── Step 7: Print comparison result ───────────────────────────────
echo ""
echo "=== compare_windows Result ==="
echo "$compare_body" | pretty_json

if command -v jq &>/dev/null; then
  echo ""
  echo "--- Highlights ---"
  echo "$compare_body" | jq -r '
    if .result then .result else . end |
    if type == "string" then . else
      "New error codes:        \(.new_error_codes // .new_errors // "n/a")",
      "Increased error codes:  \(.increased // "n/a")",
      "Failures (baseline):    \(.total_failures_before // .baseline_failures // "n/a")",
      "Failures (current):     \(.total_failures_after // .current_failures // "n/a")"
    end
  ' 2>/dev/null || echo "(could not extract highlights)"
fi

# ── Step 8: Print dashboard URL ───────────────────────────────────
echo ""
echo "=== Dashboard ==="
echo "  Web UI:  ${BASE_URL}/ui/"
echo "  Overview API: ${BASE_URL}/v1/overview?window=5m"

# ── Summary ───────────────────────────────────────────────────────
echo ""
print_results
