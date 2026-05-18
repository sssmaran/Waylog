#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GATEWAY_URL="${GATEWAY_URL:-http://localhost:9081}"
INGEST_URL="${INGEST_URL:-http://localhost:8080}"
WAYLOG_READ_KEY="${WAYLOG_READ_KEY:-demo}"
WAYLOG_WRITE_KEY="${WAYLOG_WRITE_KEY:-demo}"
WAYLOG_AGENT_KEY="${WAYLOG_AGENT_KEY:-demo}"
REQUESTS="${REQUESTS:-20}"
CONCURRENCY="${CONCURRENCY:-5}"
TIMEOUT="${WAYLOG_CLI_TIMEOUT:-5s}"
PROOF_DIR="${WAYLOG_PROOF_DIR:-./data/demo-state/proof}"

CLI_BIN="${WAYLOG_CLI_BIN:-./data/demo-state/bin/waylog}"
JSON_BIN="${WAYLOG_JSON_HELPER_BIN:-./data/demo-state/bin/demo-acceptance-json}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

cleanup() {
  make demo-stop >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "[proof-loop] starting local demo"
make demo

mkdir -p ./data/demo-state/bin "$PROOF_DIR"
go build -o "$CLI_BIN" ./cmd/waylog
go build -o "$JSON_BIN" ./scripts/demo-acceptance-json

CLI=("$CLI_BIN" --addr "$INGEST_URL" --api-key "$WAYLOG_READ_KEY" --timeout "$TIMEOUT")

alert_id="alert_proof_pmt_502_$(date +%s)"
alert_timestamp="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
alert_body="{\"source\":\"waylog\",\"alert_id\":\"${alert_id}\",\"service\":\"checkout\",\"env\":\"demo\",\"severity\":\"critical\",\"reason\":\"PMT_502 spike\",\"message\":\"proof-loop alert for checkout payment failures\",\"error_code\":\"PMT_502\",\"timestamp\":\"${alert_timestamp}\"}"
alert_status="$(curl -s -o "${PROOF_DIR}/alert.json" -w "%{http_code}" \
  -X POST "${INGEST_URL}/v1/alerts" \
  -H "Authorization: Bearer ${WAYLOG_WRITE_KEY}" \
  -H 'Content-Type: application/json' \
  --data "$alert_body" || echo "000")"
[[ "$alert_status" == "201" ]] || fail "alert webhook failed: HTTP $alert_status"
echo "[proof-loop] alert accepted"

burst_body="{\"requests\":${REQUESTS},\"concurrency\":${CONCURRENCY}}"
burst_status="$(curl -s -o "${PROOF_DIR}/burst.json" -w "%{http_code}" \
  -X POST "${GATEWAY_URL}/demo/burst" \
  -H 'Content-Type: application/json' \
  --data "$burst_body" || echo "000")"
[[ "$burst_status" == "200" ]] || fail "traffic burst failed: HTTP $burst_status"
echo "[proof-loop] payment failure burst captured"

errors_json=""
for _ in $(seq 1 15); do
  errors_json="$("${CLI[@]}" --json errors --window 15m --limit 10)" || fail "waylog errors failed"
  if "$JSON_BIN" has-payment-error <<<"$errors_json"; then
    break
  fi
  sleep 1
done
"$JSON_BIN" has-payment-error <<<"$errors_json" || fail "payment_502 error family did not appear"
printf "%s\n" "$errors_json" >"${PROOF_DIR}/errors.json"

incidents_json=""
for _ in $(seq 1 20); do
  incidents_json="$("${CLI[@]}" --json incidents)" || fail "waylog incidents failed"
  if "$JSON_BIN" has-dependency-incident <<<"$incidents_json"; then
    break
  fi
  sleep 1
done
"$JSON_BIN" has-dependency-incident <<<"$incidents_json" || fail "dependency incident did not appear"
printf "%s\n" "$incidents_json" >"${PROOF_DIR}/incidents.json"

incident_id="$("$JSON_BIN" first-incident-id <<<"$incidents_json")"
[[ -n "$incident_id" ]] || fail "no incident_id found"
echo "[proof-loop] active incident: ${incident_id}"

triage_cli="$("${CLI[@]}" --json triage "$incident_id" --snapshot)" || fail "waylog triage failed"
printf "%s\n" "$triage_cli" >"${PROOF_DIR}/triage.json"
hash_cli="$("$JSON_BIN" triage-report-hash <<<"$triage_cli")"
[[ -n "$hash_cli" ]] || fail "CLI triage report_hash missing"

read_status="$(curl -s -o "${PROOF_DIR}/triage-read.json" -w "%{http_code}" \
  -H "Authorization: Bearer ${WAYLOG_READ_KEY}" \
  "${INGEST_URL}/v1/triage/${incident_id}?snapshot=true" || echo "000")"
[[ "$read_status" == "200" ]] || fail "read triage endpoint failed: HTTP $read_status"
hash_read="$("$JSON_BIN" triage-report-hash <"${PROOF_DIR}/triage-read.json")"

tool_body="{\"incident_id\":\"${incident_id}\",\"snapshot\":true}"
tool_status="$(curl -s -o "${PROOF_DIR}/triage-tool.json" -w "%{http_code}" \
  -X POST "${INGEST_URL}/v1/tools/triage_incident" \
  -H "Authorization: Bearer ${WAYLOG_AGENT_KEY}" \
  -H 'Content-Type: application/json' \
  --data "$tool_body" || echo "000")"
[[ "$tool_status" == "200" ]] || fail "triage_incident tool failed: HTTP $tool_status"
hash_tool="$("$JSON_BIN" triage-report-hash <"${PROOF_DIR}/triage-tool.json")"

plan_body="{\"template\":\"triage\",\"params\":{\"incident_id\":\"${incident_id}\",\"snapshot\":true}}"
plan_status="$(curl -s -o "${PROOF_DIR}/triage-plan.json" -w "%{http_code}" \
  -X POST "${INGEST_URL}/v1/plans/execute" \
  -H "Authorization: Bearer ${WAYLOG_AGENT_KEY}" \
  -H 'Content-Type: application/json' \
  --data "$plan_body" || echo "000")"
[[ "$plan_status" == "200" ]] || fail "triage plan template failed: HTTP $plan_status"
hash_plan="$("$JSON_BIN" plan-triage-report-hash <"${PROOF_DIR}/triage-plan.json")"

[[ "$hash_cli" == "$hash_read" && "$hash_cli" == "$hash_tool" && "$hash_cli" == "$hash_plan" ]] || {
  echo "hash_cli=$hash_cli hash_read=$hash_read hash_tool=$hash_tool hash_plan=$hash_plan" >&2
  fail "triage report_hash mismatch across surfaces"
}
echo "[proof-loop] report_hash stable across CLI/read/tool/plan: ${hash_cli}"

"$JSON_BIN" triage-has-alert-id "$alert_id" <"${PROOF_DIR}/triage.json" || fail "triage report missing current alert evidence"
"$JSON_BIN" triage-has-trace <"${PROOF_DIR}/triage.json" || fail "triage report missing trace evidence"
"$JSON_BIN" triage-has-dependency-signal <"${PROOF_DIR}/triage.json" || fail "triage report missing dependency signal"
"$JSON_BIN" triage-has-next-check <"${PROOF_DIR}/triage.json" || fail "triage report missing next checks"
trace_id="$("$JSON_BIN" triage-first-trace <"${PROOF_DIR}/triage.json")"
[[ -n "$trace_id" ]] || fail "triage report missing sample trace id"

for format in markdown slack pagerduty; do
  case "$format" in
    markdown) out="${PROOF_DIR}/report.md" ;;
    slack) out="${PROOF_DIR}/slack.json" ;;
    pagerduty) out="${PROOF_DIR}/pagerduty.txt" ;;
  esac
  report_status="$(curl -s -o "$out" -w "%{http_code}" \
    -H "Authorization: Bearer ${WAYLOG_READ_KEY}" \
    "${INGEST_URL}/v1/triage/${incident_id}/report?format=${format}&snapshot=true" || echo "000")"
  [[ "$report_status" == "200" ]] || fail "${format} report endpoint failed: HTTP $report_status"
  grep -q "$incident_id" "$out" || fail "${format} report missing incident_id citation"
  grep -q "$hash_cli" "$out" || fail "${format} report missing report_hash citation"
  grep -q "$alert_id" "$out" || fail "${format} report missing alert_id citation"
done
grep -q "$trace_id" "${PROOF_DIR}/report.md" || fail "markdown report missing trace citation"
grep -q 'signal `sig_' "${PROOF_DIR}/report.md" || fail "markdown report missing signal citation"

render_status="$(curl -s -o "${PROOF_DIR}/render-tool.json" -w "%{http_code}" \
  -X POST "${INGEST_URL}/v1/tools/render_triage_report" \
  -H "Authorization: Bearer ${WAYLOG_AGENT_KEY}" \
  -H 'Content-Type: application/json' \
  --data "{\"incident_id\":\"${incident_id}\",\"format\":\"markdown\",\"snapshot\":true}" || echo "000")"
[[ "$render_status" == "200" ]] || fail "render_triage_report tool failed: HTTP $render_status"
grep -q "$hash_cli" "${PROOF_DIR}/render-tool.json" || fail "render_triage_report tool output missing report_hash"
echo "[proof-loop] operator reports rendered with citations"

WAYLOG_ROLLUP_USE_RUNNING_DEMO=1 WAYLOG_CLI_BIN="$CLI_BIN" WAYLOG_JSON_HELPER_BIN="$JSON_BIN" \
  ./scripts/rollup-comparison.sh | tee "${PROOF_DIR}/rollup-comparison.txt"

WAYLOG_SCORECARD_USE_RUNNING_DEMO=1 WAYLOG_SCENARIO=warm-demo WAYLOG_CLI_BIN="$CLI_BIN" WAYLOG_JSON_HELPER_BIN="$JSON_BIN" WAYLOG_PROOF_DIR="$PROOF_DIR" \
  bash ./scripts/rca-scorecard.sh | tee "${PROOF_DIR}/scorecard.txt"

cat <<EOF
[proof-loop] complete
  artifacts: ${PROOF_DIR}
  report_hash: ${hash_cli}
EOF
