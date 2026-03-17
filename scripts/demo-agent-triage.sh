#!/usr/bin/env bash
# demo-agent-triage.sh — Scenario: agent triage workflow via curl
# Demonstrates: graph_insights → failure_patterns → explain_request → blast_radius
# All calls use Idempotency-Key headers. No LLM dependency.
set -euo pipefail
source "$(dirname "$0")/_lib.sh"

inject_event() {
  local service="$1" success="$2" status_code="$3" error_code="${4:-}"
  local trace_id span_id
  trace_id=$(rand_hex 32)
  span_id=$(rand_hex 16)

  local event_name="${service}.request"
  if [[ "$success" == "false" ]]; then
    event_name="${service}.error"
  fi

  local latency=$((RANDOM % 400 + 20))
  local user_id="user-$(( RANDOM % 100 ))"
  local tiers=("free" "premium" "enterprise")
  local tier="${tiers[$(( RANDOM % 3 ))]}"
  local regions=("us-east" "us-west" "eu-west")
  local region="${regions[$(( RANDOM % 3 ))]}"

  local error_block=""
  if [[ -n "$error_code" ]]; then
    error_block=",\"error\":{\"code\":\"${error_code}\",\"message\":\"error\"}"
  fi

  curl -s -o /dev/null -X POST "${BASE_URL}/v1/events" \
    -H "Content-Type: application/json" \
    -d "{
      \"schema_version\":\"1.0\",
      \"event_name\":\"${event_name}\",
      \"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%S.000Z)\",
      \"system\":{\"service\":\"${service}\",\"env\":\"demo\",\"version\":\"1.0.0\",\"deployment_id\":\"deploy_${service}\"},
      \"request\":{\"trace_id\":\"${trace_id}\",\"span_id\":\"${span_id}\",\"flow\":\"purchase\"},
      \"user\":{\"id\":\"${user_id}\",\"tier\":\"${tier}\",\"region\":\"${region}\"},
      \"outcome\":{\"status_code\":${status_code},\"success\":${success}},
      \"metrics\":{\"latency_ms\":${latency}}${error_block}
    }"
}

# Usage: do_tool <tool_name> <body> <idempotency_key>
# Sets: RESP_BODY, RESP_STATUS
do_tool() {
  local tool="$1" body="$2" idemp_key="$3"
  local raw
  raw=$(curl -s -w "\n%{http_code}" \
    -X POST "${BASE_URL}/v1/tools/${tool}" \
    -H "Content-Type: application/json" \
    -H "Idempotency-Key: ${idemp_key}" \
    -d "$body")
  RESP_STATUS=$(echo "$raw" | tail -1)
  RESP_BODY=$(echo "$raw" | sed '$d')
}

echo "╔══════════════════════════════════════════════════════════╗"
echo "║  Demo: Agent Triage — Tool Call Workflow                 ║"
echo "╚══════════════════════════════════════════════════════════╝"
echo ""

# ── Phase 1: Setup ──────────────────────────────────────────
wait_ready

echo ""
echo "» Phase 1: Injecting failure scenario..."
for i in $(seq 1 30); do inject_event "api-gateway" "true" "200"; done
for i in $(seq 1 25); do inject_event "payment-service" "false" "502" "PMT_502"; done
for i in $(seq 1 10); do inject_event "checkout-service" "false" "500" "CHK_TIMEOUT"; done
echo "  Sent 65 events (30 healthy, 35 failures)."
sleep 2

# ── Phase 2: Agent Triage Steps ─────────────────────────────
echo ""
echo "» Step 1/4: graph_insights (with Idempotency-Key)"
do_tool "graph_insights" '{"window":"10m"}' "triage-step-1"
check_status "graph_insights" "$RESP_STATUS" "200"

total_failures=$(jq_extract '.total_failures' "$RESP_BODY")
echo "  total_failures: ${total_failures:-<unavailable>}"
if command -v jq &>/dev/null; then
  top_errors=$(echo "$RESP_BODY" | jq -r '.top_errors // empty' 2>/dev/null || echo "")
  if [[ -n "$top_errors" ]]; then
    echo "  top_errors: $top_errors"
  fi
fi

echo ""
echo "» Step 2/4: failure_patterns (with Idempotency-Key)"
do_tool "failure_patterns" '{"window":"10m","limit":5}' "triage-step-2"
check_status "failure_patterns" "$RESP_STATUS" "200"

if command -v jq &>/dev/null; then
  pattern_count=$(echo "$RESP_BODY" | jq '.patterns | length' 2>/dev/null || echo "0")
  echo "  patterns found: $pattern_count"
  echo "$RESP_BODY" | jq -r '.patterns[] | "    \(.error_code): \(.count) occurrences"' 2>/dev/null || true
fi

# Grab a trace_id for explain_request from recent failed traces
example_trace=""
recent_raw=$(curl -s -w "\n%{http_code}" "${BASE_URL}/v1/traces/recent?failures_only=true&limit=1")
recent_status=$(echo "$recent_raw" | tail -1)
recent_body=$(echo "$recent_raw" | sed '$d')
if [[ "$recent_status" == "200" ]]; then
  example_trace=$(jq_extract '.traces[0].trace_id' "$recent_body")
fi
if [[ -n "$example_trace" && "$example_trace" != "null" ]]; then
  echo "  example trace for drilldown: ${example_trace}"
fi

echo ""
echo "» Step 3/4: explain_request (trace from patterns)"
if [[ -n "$example_trace" && "$example_trace" != "null" ]]; then
  do_tool "explain_request" "{\"trace_id\":\"${example_trace}\"}" "triage-step-3"
  check_status "explain_request" "$RESP_STATUS" "200"
  if command -v jq &>/dev/null; then
    echo "$RESP_BODY" | jq -r '"  verdict: \(.verdict // "n/a")"' 2>/dev/null || true
    echo "$RESP_BODY" | jq -r '"  root_cause: \(.root_cause // "n/a")"' 2>/dev/null || true
  fi
else
  echo "  SKIP: no trace_id available from failure_patterns"
fi

echo ""
echo "» Step 4/4: blast_radius for PMT_502 (with Idempotency-Key)"
do_tool "blast_radius" '{"error_code":"PMT_502","window":"10m","include_services":true}' "triage-step-4"
check_status "blast_radius" "$RESP_STATUS" "200"

affected_requests=$(jq_extract '.affected_requests' "$RESP_BODY")
affected_users=$(jq_extract '.affected_users' "$RESP_BODY")
severity=$(jq_extract '.severity_score' "$RESP_BODY")
echo "  affected_requests: ${affected_requests:-<unavailable>}"
echo "  affected_users:    ${affected_users:-<unavailable>}"
echo "  severity_score:    ${severity:-<unavailable>}"

# ── Phase 3: Idempotency Verification ───────────────────────
echo ""
echo "» Idempotency check: replay step 1 with same key..."
do_tool "graph_insights" '{"window":"10m"}' "triage-step-1"
check_status "idempotency replay (same key+body)" "$RESP_STATUS" "200"

echo ""
echo "» Idempotency check: conflict (same key, different body)..."
do_tool "graph_insights" '{"window":"5m"}' "triage-step-1"
check_status "idempotency conflict (same key, diff body)" "$RESP_STATUS" "409"

# ── Summary ──────────────────────────────────────────────────
echo ""
print_results
