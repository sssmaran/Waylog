#!/usr/bin/env bash
# End-to-end validation for Waylog Mark II v1 (Milestone 6 Task 4).
# Starts a fresh ingest server, drives three ingestion paths (Go SDK, TS SDK,
# synthetic OTLP/HTTP), then asserts tree-shaped trace story per source and
# root-cause-counted rollup (top_errors[PMT_502] == 3).
set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/_lib.sh
source "${REPO_ROOT}/scripts/_lib.sh"

PORT="${E2E_INGEST_PORT:-18080}"
BASE_URL="http://localhost:${PORT}"
export BASE_URL

WORK_DIR=""
INGEST_PID=""
cleanup() {
  if [[ -n "$INGEST_PID" ]] && kill -0 "$INGEST_PID" 2>/dev/null; then
    kill "$INGEST_PID" 2>/dev/null || true
    wait "$INGEST_PID" 2>/dev/null || true
  fi
  [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]] && rm -rf "$WORK_DIR"
}
trap cleanup EXIT

echo "=== Waylog Mark II v1 E2E ==="

for bin in go node curl; do
  command -v "$bin" >/dev/null || { echo "ERROR: missing binary: $bin"; exit 1; }
done
node_major=$(node -v | sed 's/^v//' | cut -d. -f1)
[[ "$node_major" -lt 18 ]] && { echo "ERROR: node >= 18 required, got $(node -v)"; exit 1; }
command -v jq >/dev/null || echo "WARN: jq not found — rollup assertions require jq"

VITE_NODE="${REPO_ROOT}/packages/waylog-ts/node_modules/.bin/vite-node"
[[ -x "$VITE_NODE" ]] || { echo "ERROR: vite-node missing at $VITE_NODE (run 'npm install' in packages/waylog-ts)"; exit 1; }

BIN_DIR="${REPO_ROOT}/bin"
mkdir -p "$BIN_DIR"
echo "Building binaries ..."
go build -o "${BIN_DIR}/e2e-emit" "${REPO_ROOT}/examples/cmd/e2e-emit"
go build -o "${BIN_DIR}/e2e-otlp" "${REPO_ROOT}/examples/cmd/e2e-otlp"
go build -o "${BIN_DIR}/ingest"   "${REPO_ROOT}/cmd/ingest"

WORK_DIR=$(mktemp -d -t waylog-e2e-XXXXXX)
echo "Data dir: $WORK_DIR"
INGEST_ADDR=":${PORT}" \
SNAPSHOT_PATH="${WORK_DIR}/snapshot.json" \
EVENT_LOG_DIR="${WORK_DIR}/wal" \
EVENT_LOG_SYNC=false \
HAPPY_SAMPLE_RATE_PCT=100 \
GRAPH_UI=1 \
"${BIN_DIR}/ingest" </dev/null >"${WORK_DIR}/ingest.log" 2>&1 &
INGEST_PID=$!
echo "Ingest PID: $INGEST_PID"
wait_ready

echo ""
echo "--- Go SDK: cascading failure (3 traces) ---"
GO_OUT="${WORK_DIR}/go-traces.txt"
INGEST_URL="$BASE_URL" "${BIN_DIR}/e2e-emit" >"$GO_OUT"
GO_TRACE_IDS=()
while IFS= read -r line; do
  [[ -n "$line" ]] && GO_TRACE_IDS[${#GO_TRACE_IDS[@]}]="$line"
done <"$GO_OUT"
for tid in "${GO_TRACE_IDS[@]}"; do echo "  trace_id=$tid"; done
[[ "${#GO_TRACE_IDS[@]}" -eq 3 ]] || { echo "ERROR: expected 3 Go trace_ids, got ${#GO_TRACE_IDS[@]}"; exit 1; }

echo ""
echo "--- TS SDK: single trace ---"
TS_TRACE_ID=$(cd "${REPO_ROOT}/packages/waylog-ts" && INGEST_URL="$BASE_URL" "$VITE_NODE" examples/e2e-emit.ts 2>/dev/null | tail -1)
echo "  trace_id=$TS_TRACE_ID"
[[ "${#TS_TRACE_ID}" -eq 32 ]] || { echo "ERROR: TS SDK invalid trace_id: '$TS_TRACE_ID'"; exit 1; }

echo ""
echo "--- OTLP: synthetic 2-span trace ---"
OTLP_TRACE_ID=$(INGEST_URL="$BASE_URL" "${BIN_DIR}/e2e-otlp")
echo "  trace_id=$OTLP_TRACE_ID"
[[ "${#OTLP_TRACE_ID}" -eq 32 ]] || { echo "ERROR: OTLP invalid trace_id: '$OTLP_TRACE_ID'"; exit 1; }

# Drain WAL + counter refresh.
sleep 2

echo ""
echo "--- /v1/traces/story?format=tree per source ---"
assert_tree() {
  local label="$1" tid="$2"
  local body status
  body=$(curl -s -w $'\n%{http_code}' "${BASE_URL}/v1/traces/story?trace_id=${tid}&format=tree")
  status=$(echo "$body" | tail -n1)
  body=$(echo "$body" | sed '$d')
  if [[ "$status" != "200" ]]; then
    echo "  FAIL: $label story fetch (HTTP $status)"
    _failed=$((_failed + 1)); return
  fi
  if ! command -v jq >/dev/null; then
    echo "  PASS: $label story fetch (HTTP 200, jq not available)"
    _passed=$((_passed + 1)); return
  fi
  local chain_len tree_len
  chain_len=$(echo "$body" | jq '(.story.chain // []) | length')
  tree_len=$(echo "$body" | jq '(.story.tree // []) | length')
  if [[ "$chain_len" -gt 0 && "$tree_len" -gt 0 ]]; then
    echo "  PASS: $label story tree (chain=$chain_len tree=$tree_len)"
    _passed=$((_passed + 1))
  else
    echo "  FAIL: $label story tree (chain=$chain_len tree=$tree_len)"
    _failed=$((_failed + 1))
  fi
}
for tid in "${GO_TRACE_IDS[@]}"; do assert_tree "go[${tid:0:8}]" "$tid"; done
assert_tree "ts[${TS_TRACE_ID:0:8}]" "$TS_TRACE_ID"
assert_tree "otlp[${OTLP_TRACE_ID:0:8}]" "$OTLP_TRACE_ID"

echo ""
echo "--- Rollup: top_errors must be root-cause-counted ---"
INSIGHTS=$(curl -s "${BASE_URL}/v1/tools/graph_insights" -H 'Content-Type: application/json' -d '{"window":"10m","top_errors":10}')
if command -v jq >/dev/null; then
  count_for() {
    echo "$INSIGHTS" | jq -r --arg c "$1" '((.data // .).top_errors // .top_errors // []) | map(select(.error_code==$c)) | .[0].count // 0'
  }
  PMT=$(count_for PMT_502)
  CHK=$(count_for CHK_DOWNSTREAM)
  GW=$(count_for GW_DOWNSTREAM)
  TOTAL=$(echo "$INSIGHTS" | jq -r '(.data // .).total_failures // .total_failures // 0')
  assert_eq() {
    local desc="$1" got="$2" want="$3"
    if [[ "$got" == "$want" ]]; then
      echo "  PASS: $desc (got $got)"; _passed=$((_passed + 1))
    else
      echo "  FAIL: $desc (got $got, want $want)"
      echo "  raw insights: $INSIGHTS"
      _failed=$((_failed + 1))
    fi
  }
  assert_eq "top_errors[PMT_502] == 3 (root-cause-counted)" "$PMT" "3"
  assert_eq "top_errors[CHK_DOWNSTREAM] == 0 (propagated code excluded)" "$CHK" "0"
  assert_eq "top_errors[GW_DOWNSTREAM] == 0 (propagated code excluded)" "$GW" "0"
  assert_eq "total_failures == 3" "$TOTAL" "3"
else
  echo "  SKIP: jq required for rollup assertions. Raw: $INSIGHTS"
fi

echo ""
print_results
