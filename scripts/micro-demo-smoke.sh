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

echo ""
echo "=== Results: $passed passed, $failed failed ==="

if [[ $failed -gt 0 ]]; then
  exit 1
fi
