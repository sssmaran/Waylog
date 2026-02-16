#!/usr/bin/env bash
set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:9081}"
INGEST_URL="${INGEST_URL:-http://localhost:8080}"

passed=0
failed=0

check() {
  local desc="$1"
  local url="$2"
  local expected_status="$3"

  status=$(curl -s -o /dev/null -w "%{http_code}" "$url" || echo "000")
  if [[ "$status" == "$expected_status" ]]; then
    echo "PASS: $desc (HTTP $status)"
    passed=$((passed + 1))
  else
    echo "FAIL: $desc (expected HTTP $expected_status, got $status)"
    failed=$((failed + 1))
  fi
}

echo "=== Micro-Demo Smoke Tests ==="
echo ""

# Test 1: Gateway UI
check "Gateway UI loads" "${GATEWAY_URL}/demo" "200"

# Test 2: Successful purchase
check "Purchase success" "${GATEWAY_URL}/purchase" "200"

# Test 3: Payment failure
check "Purchase with payment_fail" "${GATEWAY_URL}/purchase?force=payment_fail" "502"

# Test 4: Checkout failure
check "Purchase with checkout_fail" "${GATEWAY_URL}/purchase?force=checkout_fail" "500"

# Test 5: Ingest health (if running)
check "Ingest health" "${INGEST_URL}/healthz" "200"

# Wait for events to flush through Kafka -> bridge -> ingest
sleep 3

# Extract a trace_id from a purchase response
resp=$(curl -s "${GATEWAY_URL}/purchase")
if command -v jq &>/dev/null; then
  trace_id=$(echo "$resp" | jq -r '.trace_id // empty')
else
  trace_id=$(echo "$resp" | grep -o '"trace_id":"[^"]*"' | head -1 | sed 's/.*":"//' | sed 's/"//')
fi

sleep 2

# Test 6: Overview API
check "Overview API" "${INGEST_URL}/v1/overview?window=5m" "200"

# Test 7: Recent traces API
check "Recent traces API" "${INGEST_URL}/v1/traces/recent?limit=5" "200"

# Test 8: Trace story API (requires valid trace_id)
if [[ -n "${trace_id:-}" ]]; then
  check "Trace story API" "${INGEST_URL}/v1/traces/story?trace_id=${trace_id}" "200"
else
  echo "SKIP: Trace story API (no trace_id captured)"
fi

# Test 9: Trace story 404 for unknown trace
check "Trace story 404" "${INGEST_URL}/v1/traces/story?trace_id=00000000000000000000000000000000" "404"

# Test 10: Trace story 400 for missing param
check "Trace story 400" "${INGEST_URL}/v1/traces/story" "400"

echo ""
echo "=== Results: $passed passed, $failed failed ==="

if [[ $failed -gt 0 ]]; then
  exit 1
fi
