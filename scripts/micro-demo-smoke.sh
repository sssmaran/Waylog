#!/usr/bin/env bash
set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:9081}"
INGEST_URL="${INGEST_URL:-http://localhost:8080}"
WAYLOG_READ_KEY="${WAYLOG_READ_KEY:-demo}"

passed=0
failed=0

record_status() {
  local desc="$1"
  local status="$2"
  local expected_status="$3"

  if [[ "$status" == "$expected_status" ]]; then
    echo "PASS: $desc (HTTP $status)"
    passed=$((passed + 1))
  else
    echo "FAIL: $desc (expected HTTP $expected_status, got $status)"
    failed=$((failed + 1))
  fi
}

check() {
	local desc="$1"
	local url="$2"
	local expected_status="$3"

	status=$(curl -s -o /dev/null -w "%{http_code}" "$url" || echo "000")
	record_status "$desc" "$status" "$expected_status"
}

post_purchase() {
  local scenario="$1"
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST "${GATEWAY_URL}/purchase" \
    -H 'Content-Type: application/json' \
    --data "{\"sku\":\"X1\",\"scenario\":\"${scenario}\"}" || echo "000"
}

check_read() {
  local desc="$1"
  local url="$2"
	local expected_status="$3"

	status=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer ${WAYLOG_READ_KEY}" "$url" || echo "000")
	record_status "$desc" "$status" "$expected_status"
}

echo "=== Micro-Demo Smoke Tests ==="
echo ""

# Test 1: Gateway UI
check "Gateway UI loads" "${GATEWAY_URL}/demo" "200"

# Test 2: Successful purchase
record_status "Purchase happy" "$(post_purchase "happy")" "200"

# Test 3: Payment failure
record_status "Purchase payment_502" "$(post_purchase "payment_502")" "502"

# Test 4: Suppressed payment failure
record_status "Purchase suppressed_payment_502" "$(post_purchase "suppressed_payment_502")" "502"

# Test 5: Ingest health (if running)
check "Ingest health" "${INGEST_URL}/healthz" "200"

# Wait for SDK delivery into ingest.
sleep 3

# Extract a trace_id from a purchase response
resp=$(curl -s -X POST "${GATEWAY_URL}/purchase" -H 'Content-Type: application/json' --data '{"sku":"X1","scenario":"payment_502"}')
if command -v jq &>/dev/null; then
  trace_id=$(echo "$resp" | jq -r '.trace_id // empty')
else
  trace_id=$(echo "$resp" | grep -o '"trace_id":"[^"]*"' | head -1 | sed 's/.*":"//' | sed 's/"//')
fi

sleep 2

# Test 6: Errors API
check_read "Errors API" "${INGEST_URL}/v1/errors?window=15m" "200"

# Test 7: Recent traces API
check_read "Recent traces API" "${INGEST_URL}/v1/traces/recent?limit=5" "200"

# Test 8: Trace story API (requires valid trace_id)
if [[ -n "${trace_id:-}" ]]; then
  check_read "Trace story API" "${INGEST_URL}/v1/traces/story?trace_id=${trace_id}" "200"
else
  echo "SKIP: Trace story API (no trace_id captured)"
fi

# Test 9: Trace story 404 for unknown trace
check_read "Trace story 404" "${INGEST_URL}/v1/traces/story?trace_id=00000000000000000000000000000000" "404"

# Test 10: Trace story 400 for missing param
check_read "Trace story 400" "${INGEST_URL}/v1/traces/story" "400"

echo ""
echo "=== Results: $passed passed, $failed failed ==="

if [[ $failed -gt 0 ]]; then
  exit 1
fi
