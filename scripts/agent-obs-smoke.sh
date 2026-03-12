#!/usr/bin/env bash
set -euo pipefail

# Smoke test for agent-obs integration.
# Requires waylog-agentobs server running on localhost:9091.
# Usage: AGENT_OBS_URL=http://localhost:9091 ./scripts/agent-obs-smoke.sh

BASE="${AGENT_OBS_URL:-http://localhost:9091}"
PASS=0
FAIL=0

check() {
  local desc="$1" ok="$2"
  if [ "$ok" = "true" ]; then
    echo "  PASS: $desc"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $desc"
    FAIL=$((FAIL + 1))
  fi
}

echo "=== Agent-Obs Smoke Test ==="
echo "Target: $BASE"
echo ""

# 1. Liveness
echo "--- Probes ---"
LIVEZ=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/livez")
check "/livez returns 200" "$([ "$LIVEZ" = "200" ] && echo true || echo false)"

# 2. Ingest a minimal run (6 events: run.start, session.start, step.start, step.end, session.end, run.end)
echo ""
echo "--- Ingest a complete run ---"
NOW=$(date -u +"%Y-%m-%dT%H:%M:%S.000000000Z")
RUN_ID="smoke-run-$(date +%s)"
SESSION_ID="smoke-sess-$(date +%s)"
STEP_ID="smoke-step-$(date +%s)"

BODY=$(cat <<EOF
[
  {"event_id":"e1-$RUN_ID","run_id":"$RUN_ID","event_type":"run.start","timestamp":"$NOW","schema_version":"1.0","run_name":"smoke-test"},
  {"event_id":"e2-$RUN_ID","run_id":"$RUN_ID","session_id":"$SESSION_ID","event_type":"session.start","timestamp":"$NOW","schema_version":"1.0","agent_name":"smoke-agent"},
  {"event_id":"e3-$RUN_ID","run_id":"$RUN_ID","session_id":"$SESSION_ID","step_id":"$STEP_ID","step_index":0,"step_name":"llm-call","event_type":"step.start","timestamp":"$NOW","schema_version":"1.0"},
  {"event_id":"e4-$RUN_ID","run_id":"$RUN_ID","session_id":"$SESSION_ID","step_id":"$STEP_ID","step_index":0,"step_name":"llm-call","event_type":"step.end","timestamp":"$NOW","schema_version":"1.0","latency_ms":150},
  {"event_id":"e5-$RUN_ID","run_id":"$RUN_ID","session_id":"$SESSION_ID","event_type":"session.end","timestamp":"$NOW","schema_version":"1.0","success":true},
  {"event_id":"e6-$RUN_ID","run_id":"$RUN_ID","event_type":"run.end","timestamp":"$NOW","schema_version":"1.0","success":true}
]
EOF
)

RESP=$(curl -s -w "\n%{http_code}" -X POST "$BASE/v1/agent/events" -H "Content-Type: application/json" -d "$BODY")
HTTP_CODE=$(echo "$RESP" | tail -1)
RESP_BODY=$(echo "$RESP" | head -1)

check "POST /v1/agent/events returns 202" "$([ "$HTTP_CODE" = "202" ] && echo true || echo false)"

# Parse accepted count (jq with grep fallback)
if command -v jq &>/dev/null; then
  ACCEPTED=$(echo "$RESP_BODY" | jq -r '.accepted // 0')
else
  ACCEPTED=$(echo "$RESP_BODY" | grep -o '"accepted":[0-9]*' | grep -o '[0-9]*')
fi
check "6 events accepted" "$([ "$ACCEPTED" = "6" ] && echo true || echo false)"

# 3. Dedup: re-send same events
echo ""
echo "--- Dedup ---"
RESP2=$(curl -s -w "\n%{http_code}" -X POST "$BASE/v1/agent/events" -H "Content-Type: application/json" -d "$BODY")
HTTP2=$(echo "$RESP2" | tail -1)
BODY2=$(echo "$RESP2" | head -1)
if command -v jq &>/dev/null; then
  DUP=$(echo "$BODY2" | jq -r '.duplicated // 0')
else
  DUP=$(echo "$BODY2" | grep -o '"duplicated":[0-9]*' | grep -o '[0-9]*')
fi
check "6 events deduplicated on re-send" "$([ "$DUP" = "6" ] && echo true || echo false)"

# 4. Read APIs
echo ""
echo "--- Read APIs ---"
RUNS_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/agent/runs")
check "GET /v1/agent/runs returns 200" "$([ "$RUNS_CODE" = "200" ] && echo true || echo false)"

STATS_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/v1/agent/stats")
check "GET /v1/agent/stats returns 200" "$([ "$STATS_CODE" = "200" ] && echo true || echo false)"

# 5. Dashboard
echo ""
echo "--- Dashboard ---"
UI_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/ui/")
check "GET /ui/ returns 200" "$([ "$UI_CODE" = "200" ] && echo true || echo false)"

# Summary
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
