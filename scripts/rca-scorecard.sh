#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GATEWAY_URL="${GATEWAY_URL:-http://localhost:9081}"
INGEST_URL="${INGEST_URL:-http://localhost:8080}"
WAYLOG_READ_KEY="${WAYLOG_READ_KEY:-demo}"
WAYLOG_WRITE_KEY="${WAYLOG_WRITE_KEY:-demo}"
REQUESTS="${REQUESTS:-20}"
CONCURRENCY="${CONCURRENCY:-5}"
TIMEOUT="${WAYLOG_CLI_TIMEOUT:-5s}"
USE_RUNNING="${WAYLOG_SCORECARD_USE_RUNNING_DEMO:-0}"
PROOF_DIR="${WAYLOG_PROOF_DIR:-./data/demo-state/proof}"

CLI_BIN="${WAYLOG_CLI_BIN:-./data/demo-state/bin/waylog}"
JSON_BIN="${WAYLOG_JSON_HELPER_BIN:-./data/demo-state/bin/demo-acceptance-json}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

http_code() {
  curl -s -o /dev/null -w "%{http_code}" "$1" || echo "000"
}

bool_word() {
  if "$@"; then
    echo true
  else
    echo false
  fi
}

now_ms() {
  perl -MTime::HiRes=time -e 'printf "%.0f\n", time() * 1000'
}

cleanup() {
  if [[ "$USE_RUNNING" != "1" ]]; then
    make demo-stop >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if [[ "$USE_RUNNING" != "1" ]]; then
  make demo
elif [[ "$(http_code "${GATEWAY_URL}/demo")" != "200" ]] || [[ "$(http_code "${INGEST_URL}/healthz")" != "200" ]]; then
  fail "running demo is not reachable. Start it with make demo or unset WAYLOG_SCORECARD_USE_RUNNING_DEMO"
fi

mkdir -p ./data/demo-state/bin "$PROOF_DIR"
go build -o "$CLI_BIN" ./cmd/waylog
go build -o "$JSON_BIN" ./scripts/demo-acceptance-json

CLI=("$CLI_BIN" --addr "$INGEST_URL" --api-key "$WAYLOG_READ_KEY" --timeout "$TIMEOUT")

alert_id="alert_scorecard_pmt_502_$(date +%s)"
alert_timestamp="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
alert_body="{\"source\":\"waylog\",\"alert_id\":\"${alert_id}\",\"service\":\"checkout\",\"env\":\"demo\",\"severity\":\"critical\",\"reason\":\"PMT_502 spike\",\"message\":\"scorecard alert for checkout payment failures\",\"error_code\":\"PMT_502\",\"timestamp\":\"${alert_timestamp}\"}"
alert_status="$(curl -s -o "${PROOF_DIR}/scorecard-alert.json" -w "%{http_code}" \
  -X POST "${INGEST_URL}/v1/alerts" \
  -H "Authorization: Bearer ${WAYLOG_WRITE_KEY}" \
  -H 'Content-Type: application/json' \
  --data "$alert_body" || echo "000")"
[[ "$alert_status" == "201" ]] || fail "alert webhook failed: HTTP $alert_status"

burst_body="{\"requests\":${REQUESTS},\"concurrency\":${CONCURRENCY}}"
burst_status="$(curl -s -o "${PROOF_DIR}/scorecard-burst.json" -w "%{http_code}" \
  -X POST "${GATEWAY_URL}/demo/burst" \
  -H 'Content-Type: application/json' \
  --data "$burst_body" || echo "000")"
[[ "$burst_status" == "200" ]] || fail "traffic burst failed: HTTP $burst_status"
answer_start="$(now_ms)"

errors_json=""
for _ in $(seq 1 15); do
  errors_json="$("${CLI[@]}" --json errors --window 15m --limit 10)" || fail "waylog errors failed"
  if "$JSON_BIN" has-payment-error <<<"$errors_json"; then
    break
  fi
  sleep 1
done
"$JSON_BIN" has-payment-error <<<"$errors_json" || fail "payment_502 error family did not appear"
printf "%s\n" "$errors_json" >"${PROOF_DIR}/scorecard-errors.json"

incidents_json=""
for _ in $(seq 1 20); do
  incidents_json="$("${CLI[@]}" --json incidents)" || fail "waylog incidents failed"
  if "$JSON_BIN" has-dependency-incident <<<"$incidents_json"; then
    break
  fi
  sleep 1
done
"$JSON_BIN" has-dependency-incident <<<"$incidents_json" || fail "dependency incident did not appear"
printf "%s\n" "$incidents_json" >"${PROOF_DIR}/scorecard-incidents.json"

incident_id="$("$JSON_BIN" first-incident-id <<<"$incidents_json")"
[[ -n "$incident_id" ]] || fail "no incident id found"

triage_a="$("${CLI[@]}" --json triage "$incident_id" --snapshot)" || fail "waylog triage failed"
triage_b="$("${CLI[@]}" --json triage "$incident_id" --snapshot)" || fail "second waylog triage failed"
answer_end="$(now_ms)"
printf "%s\n" "$triage_a" >"${PROOF_DIR}/scorecard-triage.json"

hash_a="$("$JSON_BIN" triage-report-hash <<<"$triage_a")"
hash_b="$("$JSON_BIN" triage-report-hash <<<"$triage_b")"
hash_stable=true
if [[ -z "$hash_a" || "$hash_a" != "$hash_b" ]]; then
  hash_stable=false
fi

scenario="${WAYLOG_SCENARIO:-cold-demo}"

blast_json="$("${CLI[@]}" --json blast checkout:payment.charge:PMT_502 --window 15m)" || fail "waylog blast failed"
printf "%s\n" "$blast_json" >"${PROOF_DIR}/scorecard-blast.json"

root_count="$("$JSON_BIN" payment-error-count <<<"$errors_json")"
affected_services="$("$JSON_BIN" blast-affected-services <<<"$blast_json")"
[[ "$root_count" =~ ^[0-9]+$ && "$affected_services" =~ ^[0-9]+$ ]] || fail "non-numeric scorecard counts"
(( root_count > 0 )) || fail "root-cause count is empty"
(( affected_services > 1 )) || fail "blast radius did not show cross-service spread"
naive_count=$((root_count * affected_services))
inflation_avoided=$((naive_count - root_count))
time_to_answer_ms=$((answer_end - answer_start))

root_cause_accuracy="$(bool_word "$JSON_BIN" triage-root-cause-accurate <<<"$triage_a")"
cause_classification="$(bool_word "$JSON_BIN" incident-cause-is-dependency "$incident_id" <<<"$incidents_json")"
trace_present="$(bool_word "$JSON_BIN" triage-has-trace <<<"$triage_a")"
dependency_signal_present="$(bool_word "$JSON_BIN" triage-has-dependency-signal <<<"$triage_a")"
alert_present="$(bool_word "$JSON_BIN" triage-has-alert <<<"$triage_a")"
next_checks_present="$(bool_word "$JSON_BIN" triage-has-next-check <<<"$triage_a")"

cat >"${PROOF_DIR}/scorecard.json" <<EOF
{
  "root_cause_accuracy": ${root_cause_accuracy},
  "cause_classification": ${cause_classification},
  "evidence_completeness": {
    "trace_present": ${trace_present},
    "dependency_signal_present": ${dependency_signal_present},
    "alert_present": ${alert_present},
    "next_checks_present": ${next_checks_present}
  },
  "scenario": "${scenario}",
  "triage_latency_ms": ${time_to_answer_ms},
  "report_hash_stable": ${hash_stable},
  "propagated_error_inflation_avoided": ${inflation_avoided},
  "root_cause_count": ${root_count},
  "naive_propagated_count": ${naive_count},
  "report_hash": "${hash_a}"
}
EOF

cat <<EOF
RCA scorecard
  root cause accuracy: ${root_cause_accuracy}
  cause classification dependency: ${cause_classification}
  evidence completeness: trace=${trace_present} dependency_signal=${dependency_signal_present} alert=${alert_present} next_checks=${next_checks_present}
  triage latency: ${time_to_answer_ms}ms
  report hash stable: ${hash_stable} (${hash_a})
  propagated-error inflation avoided: ${inflation_avoided} (${naive_count} naive - ${root_count} root-cause)
  scenario: ${scenario}

Artifacts:
  ${PROOF_DIR}/scorecard.json
EOF

if [[ "$hash_stable" != "true" ]]; then
  echo "FAIL: triage report hash unstable: A=$hash_a B=$hash_b" >&2
  exit 1
fi
