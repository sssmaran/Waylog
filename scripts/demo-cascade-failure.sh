#!/usr/bin/env bash
# demo-cascade-failure.sh — Scenario: db timeout → checkout/payment degrade → failure chain
set -euo pipefail
source "$(dirname "$0")/_lib.sh"

send_event() {
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST "${BASE_URL}/v1/events" \
    -H "Content-Type: application/json" \
    -d "$1"
}

echo "=== Cascade Failure Demo ==="
echo "    db timeout -> checkout/payment degrade -> failure chain"
echo ""

# ── Step 1: Wait for stack ready ──────────────────────────────────────
wait_ready
echo ""

# ── Step 2: Send 30 healthy baseline events ──────────────────────────
echo "Sending 30 healthy baseline events for api-gateway ..."
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)

for i in $(seq 1 30); do
  trace_id=$(rand_hex 32)
  span_id=$(rand_hex 16)

  rc=$(send_event "{
    \"schema_version\": \"1.0\",
    \"event_name\": \"api-gateway.request\",
    \"timestamp\": \"${now}\",
    \"system\": {
      \"service\": \"api-gateway\",
      \"env\": \"demo\"
    },
    \"request\": {
      \"trace_id\": \"${trace_id}\",
      \"span_id\": \"${span_id}\",
      \"flow\": \"purchase\"
    },
    \"user\": {
      \"id\": \"user-baseline-${i}\",
      \"tier\": \"free\",
      \"region\": \"us-east-1\"
    },
    \"outcome\": {
      \"status_code\": 200,
      \"success\": true
    },
    \"metrics\": {
      \"latency_ms\": $((50 + RANDOM % 100))
    }
  }")

  if [[ "$rc" != "202" && "$rc" != "200" ]]; then
    echo "  WARN: baseline event $i returned HTTP $rc"
  fi
done
echo "  Done (30 baseline events sent)."
echo ""

# ── Step 3: Send 15 cascade failure traces ────────────────────────────
echo "Sending 15 cascade failure traces (3 spans each) ..."
last_trace_id=""

for i in $(seq 1 15); do
  trace_id=$(rand_hex 32)
  last_trace_id="$trace_id"

  root_span=$(rand_hex 16)
  checkout_span=$(rand_hex 16)
  payment_span=$(rand_hex 16)

  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  # Span 1: api-gateway (root) — 502 UPSTREAM_502
  send_event "{
    \"schema_version\": \"1.0\",
    \"event_name\": \"api-gateway.error\",
    \"timestamp\": \"${ts}\",
    \"system\": {
      \"service\": \"api-gateway\",
      \"env\": \"demo\"
    },
    \"request\": {
      \"trace_id\": \"${trace_id}\",
      \"span_id\": \"${root_span}\",
      \"flow\": \"purchase\"
    },
    \"user\": {
      \"id\": \"user-cascade-${i}\",
      \"tier\": \"premium\",
      \"region\": \"us-east-1\"
    },
    \"outcome\": {
      \"status_code\": 502,
      \"success\": false
    },
    \"error\": {
      \"code\": \"UPSTREAM_502\",
      \"message\": \"upstream checkout-service returned 502\"
    },
    \"metrics\": {
      \"latency_ms\": $((3000 + RANDOM % 2000))
    }
  }" > /dev/null

  # Span 2: checkout-service (child) — 502 DB_TIMEOUT
  send_event "{
    \"schema_version\": \"1.0\",
    \"event_name\": \"checkout-service.error\",
    \"timestamp\": \"${ts}\",
    \"system\": {
      \"service\": \"checkout-service\",
      \"env\": \"demo\",
      \"caller_service\": \"api-gateway\"
    },
    \"request\": {
      \"trace_id\": \"${trace_id}\",
      \"span_id\": \"${checkout_span}\",
      \"parent_span_id\": \"${root_span}\",
      \"flow\": \"purchase\"
    },
    \"user\": {
      \"id\": \"user-cascade-${i}\",
      \"tier\": \"premium\",
      \"region\": \"us-east-1\"
    },
    \"outcome\": {
      \"status_code\": 502,
      \"success\": false
    },
    \"error\": {
      \"code\": \"DB_TIMEOUT\",
      \"message\": \"database connection timed out after 5000ms\"
    },
    \"metrics\": {
      \"latency_ms\": $((5000 + RANDOM % 1000))
    }
  }" > /dev/null

  # Span 3: payment-service (leaf) — 503 DB_TIMEOUT
  send_event "{
    \"schema_version\": \"1.0\",
    \"event_name\": \"payment-service.error\",
    \"timestamp\": \"${ts}\",
    \"system\": {
      \"service\": \"payment-service\",
      \"env\": \"demo\",
      \"caller_service\": \"checkout-service\"
    },
    \"request\": {
      \"trace_id\": \"${trace_id}\",
      \"span_id\": \"${payment_span}\",
      \"parent_span_id\": \"${checkout_span}\",
      \"flow\": \"purchase\"
    },
    \"user\": {
      \"id\": \"user-cascade-${i}\",
      \"tier\": \"premium\",
      \"region\": \"us-east-1\"
    },
    \"outcome\": {
      \"status_code\": 503,
      \"success\": false
    },
    \"error\": {
      \"code\": \"DB_TIMEOUT\",
      \"message\": \"database connection pool exhausted\"
    },
    \"metrics\": {
      \"latency_ms\": $((5000 + RANDOM % 1000))
    }
  }" > /dev/null
done
echo "  Done (15 traces x 3 spans = 45 failure events sent)."
echo ""

# Let events settle
sleep 2

# ── Step 4: Verify endpoints ─────────────────────────────────────────
echo "=== Verification ==="
echo ""

check "Overview API" "${BASE_URL}/v1/overview?window=5m" "200"
check "Trace story for last cascade trace" "${BASE_URL}/v1/traces/story?trace_id=${last_trace_id}" "200"

echo ""

# ── Step 5: Print trace story ─────────────────────────────────────────
echo "=== Trace Story (trace_id=${last_trace_id}) ==="
echo ""
curl -s "${BASE_URL}/v1/traces/story?trace_id=${last_trace_id}" | pretty_json
echo ""

# ── Step 6: Dashboard URL ────────────────────────────────────────────
echo ""
echo "=== Dashboard ==="
echo "  ${BASE_URL}/ui/"
echo ""

print_results
