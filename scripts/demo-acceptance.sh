#!/usr/bin/env bash
set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:9081}"
INGEST_URL="${INGEST_URL:-http://localhost:8080}"
WAYLOG_READ_KEY="${WAYLOG_READ_KEY:-demo}"
WAYLOG_WRITE_KEY="${WAYLOG_WRITE_KEY:-demo}"
REQUESTS="${REQUESTS:-20}"
CONCURRENCY="${CONCURRENCY:-5}"
TIMEOUT="${WAYLOG_CLI_TIMEOUT:-5s}"
CLI_BIN="${WAYLOG_CLI_BIN:-}"
JSON_BIN="${WAYLOG_JSON_HELPER_BIN:-}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

http_code() {
  curl -s -o /dev/null -w "%{http_code}" "$1" || echo "000"
}

json_has_payment_error() {
  "$JSON_BIN" has-payment-error
}

json_first_payment_trace() {
  "$JSON_BIN" first-payment-trace
}

json_first_event_id() {
  "$JSON_BIN" first-event-id
}

json_burst_signals_accepted() {
  "$JSON_BIN" burst-signals-accepted
}

json_has_dependency_incident() {
  "$JSON_BIN" has-dependency-incident
}

json_first_incident_id() {
  "$JSON_BIN" first-incident-id
}

json_triage_report_hash() {
  "$JSON_BIN" triage-report-hash
}

if [[ "$(http_code "${GATEWAY_URL}/demo")" != "200" ]] || [[ "$(http_code "${INGEST_URL}/healthz")" != "200" ]]; then
  fail "demo stack is not running. Start it with: make demo"
fi
echo "PASS: demo stack is reachable"

if [[ -z "$CLI_BIN" ]]; then
  mkdir -p ./data/demo-state/bin
  CLI_BIN="./data/demo-state/bin/waylog"
  go build -o "$CLI_BIN" ./cmd/waylog
elif [[ ! -x "$CLI_BIN" ]]; then
  fail "WAYLOG_CLI_BIN is not executable: $CLI_BIN"
fi

if [[ -z "$JSON_BIN" ]]; then
  mkdir -p ./data/demo-state/bin
  JSON_BIN="./data/demo-state/bin/demo-acceptance-json"
  go build -o "$JSON_BIN" ./scripts/demo-acceptance-json
elif [[ ! -x "$JSON_BIN" ]]; then
  fail "WAYLOG_JSON_HELPER_BIN is not executable: $JSON_BIN"
fi

CLI=("$CLI_BIN" --addr "$INGEST_URL" --api-key "$WAYLOG_READ_KEY" --timeout "$TIMEOUT")

"${CLI[@]}" --json capabilities >/dev/null || fail "waylog capabilities failed"
echo "PASS: waylog capabilities"

alert_id="alert_demo_pmt_502"
alert_timestamp="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
alert_body="{\"source\":\"waylog\",\"alert_id\":\"${alert_id}\",\"service\":\"checkout\",\"env\":\"demo\",\"severity\":\"critical\",\"reason\":\"PMT_502 spike\",\"message\":\"demo alert for checkout payment failures\",\"error_code\":\"PMT_502\",\"timestamp\":\"${alert_timestamp}\"}"
alert_status="$(curl -s -o /tmp/waylog-demo-alert.json -w "%{http_code}" \
  -X POST "${INGEST_URL}/v1/alerts" \
  -H "Authorization: Bearer ${WAYLOG_WRITE_KEY}" \
  -H 'Content-Type: application/json' \
  --data "$alert_body" || echo "000")"
[[ "$alert_status" == "201" ]] || fail "alert webhook failed: HTTP $alert_status"
grep -q '"signal_id"' /tmp/waylog-demo-alert.json || fail "alert webhook response did not include a signal"
grep -q '"matched"' /tmp/waylog-demo-alert.json || fail "alert webhook response did not include match state"
echo "PASS: alert webhook accepted"

burst_body="{\"requests\":${REQUESTS},\"concurrency\":${CONCURRENCY}}"
burst_status="$(curl -s -o /tmp/waylog-demo-burst.json -w "%{http_code}" \
  -X POST "${GATEWAY_URL}/demo/burst" \
  -H 'Content-Type: application/json' \
  --data "$burst_body" || echo "000")"
[[ "$burst_status" == "200" ]] || fail "traffic burst failed: HTTP $burst_status"
echo "PASS: traffic burst captured (${REQUESTS} requests / ${CONCURRENCY} concurrency)"

json_burst_signals_accepted </tmp/waylog-demo-burst.json || fail "demo burst did not accept deploy and dependency signals"
echo "PASS: demo signals accepted"

errors_json=""
for _ in $(seq 1 12); do
  errors_json="$("${CLI[@]}" --json errors --window 15m --limit 10)" || fail "waylog errors failed"
  if json_has_payment_error <<<"$errors_json"; then
    break
  fi
  sleep 1
done
json_has_payment_error <<<"$errors_json" || fail "payment_502 error family did not appear in /v1/errors"
echo "PASS: waylog errors contains checkout:payment.charge:PMT_502"

"${CLI[@]}" --json recent --limit 5 >/dev/null || fail "waylog recent failed"
echo "PASS: waylog recent"

"${CLI[@]}" --json blast checkout:payment.charge:PMT_502 --window 15m >/dev/null || fail "waylog blast failed"
echo "PASS: waylog blast"

trace_id="$(json_first_payment_trace <<<"$errors_json")"
[[ -n "$trace_id" ]] || fail "no sample trace_id found for payment_502"

"${CLI[@]}" --json explain "$trace_id" >/dev/null || fail "waylog explain failed for trace $trace_id"
echo "PASS: waylog explain"

search_json="$("${CLI[@]}" --json search PMT_502 --window 15m --limit 5)" || fail "waylog search failed"
event_id="$(json_first_event_id <<<"$search_json")"
[[ -n "$event_id" ]] || fail "no sample event_id found for PMT_502"

"${CLI[@]}" --json event "$event_id" >/dev/null || fail "waylog event failed for event $event_id"
echo "PASS: waylog event"

incidents_json=""
for _ in $(seq 1 20); do
  incidents_json="$("${CLI[@]}" --json incidents)" || fail "waylog incidents failed"
  if json_has_dependency_incident <<<"$incidents_json"; then
    break
  fi
  sleep 1
done
json_has_dependency_incident <<<"$incidents_json" || fail "dependency incident did not appear in /v1/incidents/active"
echo "PASS: waylog incidents contains active dependency incident"

incident_id="$(json_first_incident_id <<<"$incidents_json")"
[[ -n "$incident_id" ]] || fail "no incident_id found for payment dependency incident"

"${CLI[@]}" --json incident "$incident_id" >/dev/null || fail "waylog incident failed for incident $incident_id"
echo "PASS: waylog incident"

snapshot="$("${CLI[@]}" incident "$incident_id" --snapshot)" || fail "waylog incident snapshot failed for incident $incident_id"
[[ "$snapshot" == *"payment.charge"* ]] || fail "incident snapshot did not mention payment.charge"
echo "PASS: waylog incident snapshot"

triage_a="$("${CLI[@]}" --json triage "$incident_id" --snapshot)" || fail "waylog triage failed for incident $incident_id"
hash_a="$(json_triage_report_hash <<<"$triage_a")"
[[ -n "$hash_a" ]] || fail "triage report_hash A is empty"

triage_b="$("${CLI[@]}" --json triage "$incident_id" --snapshot)" || fail "waylog triage second run failed for incident $incident_id"
hash_b="$(json_triage_report_hash <<<"$triage_b")"
[[ -n "$hash_b" ]] || fail "triage report_hash B is empty"

[[ "$hash_a" == "$hash_b" ]] || fail "triage report_hash unstable across runs: A=$hash_a B=$hash_b"
echo "PASS: waylog triage stable report_hash=$hash_a"

report_status="$(curl -s -o /tmp/waylog-demo-triage-report.md -w "%{http_code}" \
  -H "Authorization: Bearer ${WAYLOG_READ_KEY}" \
  "${INGEST_URL}/v1/triage/${incident_id}/report?format=markdown&snapshot=true" || echo "000")"
[[ "$report_status" == "200" ]] || fail "triage markdown report failed: HTTP $report_status"
grep -q "$hash_a" /tmp/waylog-demo-triage-report.md || fail "triage markdown report did not cite report_hash"
grep -q "$alert_id" /tmp/waylog-demo-triage-report.md || fail "triage markdown report did not cite alert evidence"
echo "PASS: triage markdown report cites alert evidence"

echo "Demo acceptance passed."
