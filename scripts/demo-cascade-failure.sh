#!/usr/bin/env bash
# demo-cascade-failure.sh — Watch a single failure propagate across three services.
#
# Scenario:
#   api-gateway  →  checkout-service  →  payment-service
#   payment's database times out. Everything upstream turns red.
#   Waylog turns the event stream into a single rendered propagation chain.
#
# This is the flagship demo for the "impact analysis engine" framing.
# It tells a story rather than dumping raw API responses.
set -euo pipefail
source "$(dirname "$0")/_lib.sh"

send_event() {
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST "${BASE_URL}/v1/events" \
    -H "Content-Type: application/json" \
    -d "$1"
}

# ─────────────────────────────────────────────────────────────
# Intro
# ─────────────────────────────────────────────────────────────
banner "WAYLOG DEMO  ·  cascade failure"
cat <<'INTRO'

  Three services. One broken dependency. One rendered answer.

    api-gateway  →  checkout-service  →  payment-service
                                          └─ database timeout

  In a log-first world you would grep three services, correlate
  by trace_id, and reconstruct the chain in your head.

  Waylog builds a live graph from the event stream. In a moment
  you will see it traverse that graph and print the propagation
  chain for a real failing trace.

INTRO

# ─────────────────────────────────────────────────────────────
# Stage 1: wait for stack
# ─────────────────────────────────────────────────────────────
section "[1/4]  waiting for the stack"
wait_ready

# ─────────────────────────────────────────────────────────────
# Stage 2: healthy baseline
# ─────────────────────────────────────────────────────────────
section "[2/4]  sending 30 healthy baseline purchases"
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
for i in $(seq 1 30); do
  trace_id=$(rand_hex 32)
  span_id=$(rand_hex 16)

  rc=$(send_event "{
    \"schema_version\": \"1.0\",
    \"event_name\": \"api-gateway.request\",
    \"timestamp\": \"${now}\",
    \"system\": {\"service\": \"api-gateway\", \"env\": \"demo\"},
    \"request\": {\"trace_id\": \"${trace_id}\", \"span_id\": \"${span_id}\", \"flow\": \"purchase\"},
    \"user\": {\"id\": \"user-baseline-${i}\", \"tier\": \"free\", \"region\": \"us-east-1\"},
    \"outcome\": {\"status_code\": 200, \"success\": true},
    \"metrics\": {\"latency_ms\": $((50 + RANDOM % 100))}
  }")

  if [[ "$rc" != "202" && "$rc" != "200" ]]; then
    echo "  WARN: baseline event $i returned HTTP $rc"
  fi
done
echo "  done  (30 events, all 200 OK)"

# ─────────────────────────────────────────────────────────────
# Stage 3: register a deploy, then inject cascading failures
# ─────────────────────────────────────────────────────────────
section "[3/4]  registering a payment-service deploy, then injecting failures"

# Register a deploy right before the failures so the dashboard's
# "What Changed" panel and the correlation line downstream have
# something real to point at. Skipped silently if cold store is off.
deploy_id="deploy-cascade-$(rand_hex 8)"
curl -s -o /dev/null -X POST "${BASE_URL}/v1/deployments" \
  -H "Content-Type: application/json" \
  -d "{\"id\":\"${deploy_id}\",\"service\":\"payment-service\",\"version\":\"v1.4.2\",\"env\":\"demo\"}" \
  || true
echo "  deploy registered: payment-service v1.4.2 (${deploy_id})"
echo ""
echo "  gateway UPSTREAM_502  →  checkout DB_TIMEOUT  →  payment DB_TIMEOUT"
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
    \"system\": {\"service\": \"api-gateway\", \"env\": \"demo\"},
    \"request\": {\"trace_id\": \"${trace_id}\", \"span_id\": \"${root_span}\", \"flow\": \"purchase\"},
    \"user\": {\"id\": \"user-cascade-${i}\", \"tier\": \"premium\", \"region\": \"us-east-1\"},
    \"outcome\": {\"status_code\": 502, \"success\": false},
    \"error\": {\"code\": \"UPSTREAM_502\", \"message\": \"upstream checkout-service returned 502\"},
    \"metrics\": {\"latency_ms\": $((3000 + RANDOM % 2000))}
  }" > /dev/null

  # Span 2: checkout-service (child) — 502 DB_TIMEOUT
  send_event "{
    \"schema_version\": \"1.0\",
    \"event_name\": \"checkout-service.error\",
    \"timestamp\": \"${ts}\",
    \"system\": {\"service\": \"checkout-service\", \"env\": \"demo\", \"caller_service\": \"api-gateway\"},
    \"request\": {\"trace_id\": \"${trace_id}\", \"span_id\": \"${checkout_span}\", \"parent_span_id\": \"${root_span}\", \"flow\": \"purchase\"},
    \"user\": {\"id\": \"user-cascade-${i}\", \"tier\": \"premium\", \"region\": \"us-east-1\"},
    \"outcome\": {\"status_code\": 502, \"success\": false},
    \"error\": {\"code\": \"DB_TIMEOUT\", \"message\": \"database connection timed out after 5000ms\"},
    \"metrics\": {\"latency_ms\": $((5000 + RANDOM % 1000))}
  }" > /dev/null

  # Span 3: payment-service (leaf) — 503 DB_TIMEOUT
  send_event "{
    \"schema_version\": \"1.0\",
    \"event_name\": \"payment-service.error\",
    \"timestamp\": \"${ts}\",
    \"system\": {\"service\": \"payment-service\", \"env\": \"demo\", \"caller_service\": \"checkout-service\"},
    \"request\": {\"trace_id\": \"${trace_id}\", \"span_id\": \"${payment_span}\", \"parent_span_id\": \"${checkout_span}\", \"flow\": \"purchase\"},
    \"user\": {\"id\": \"user-cascade-${i}\", \"tier\": \"premium\", \"region\": \"us-east-1\"},
    \"outcome\": {\"status_code\": 503, \"success\": false},
    \"error\": {\"code\": \"DB_TIMEOUT\", \"message\": \"database connection pool exhausted\"},
    \"metrics\": {\"latency_ms\": $((5000 + RANDOM % 1000))}
  }" > /dev/null
done
echo "  done  (15 traces × 3 spans = 45 failure events)"

# ─────────────────────────────────────────────────────────────
# Stage 4: let the graph settle
# ─────────────────────────────────────────────────────────────
section "[4/4]  letting the graph settle"
sleep 2
echo "  done"

# ─────────────────────────────────────────────────────────────
# The answer: rendered propagation chain
# ─────────────────────────────────────────────────────────────
section "PROPAGATION CHAIN  (last failing trace)"
curl -s "${BASE_URL}/v1/traces/story?trace_id=${last_trace_id}" | render_chain
echo ""
render_blast_radius "DB_TIMEOUT" "10m"
render_recent_deploy "payment-service" "1h"

# ─────────────────────────────────────────────────────────────
# What this shows
# ─────────────────────────────────────────────────────────────
section "WHAT THIS SHOWS"
cat <<'EXPLAIN'

  You did not grep three log streams.
  You did not correlate by trace_id across services.
  You did not ask an LLM to piece it together.

  Waylog built a live graph from the event stream and traversed
  it in O(hops). Every answer above is also a deterministic tool
  call any agent can make:

    GET  /v1/traces/story?trace_id=...        ← the chain above
    GET  /v1/blast_radius?error_code=...      ← the radius line
    POST /v1/tools/failure_chain              ← same thing, tool form
    POST /v1/tools/explain_request            ← LLM-free root cause

  Next:
    •  open http://localhost:8080/ui           (live dashboard)
    •  ./scripts/demo-agent-triage.sh          (4-step agent workflow)
    •  ./scripts/demo-comparison.sh            (before/after window diff)

EXPLAIN

# ─────────────────────────────────────────────────────────────
# Verifications (kept quiet, but fail the script if broken)
# ─────────────────────────────────────────────────────────────
check "Overview API"                     "${BASE_URL}/v1/overview?window=5m"                   "200"
check "Trace story for last cascade"     "${BASE_URL}/v1/traces/story?trace_id=${last_trace_id}" "200"

print_results
