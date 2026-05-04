#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GOCACHE_DIR="${GOCACHE:-/tmp/go-build}"
STATE_DIR="${WAYLOG_DEMO_STATE_DIR:-./data/demo-state}"
LOG_DIR="${STATE_DIR}/logs"
BIN_DIR="${STATE_DIR}/bin"

# Start from a clean local demo so the command behaves like docker-dev.
./scripts/demo-stop.sh >/dev/null 2>&1 || true
rm -rf "$STATE_DIR"
mkdir -p "$LOG_DIR" "$BIN_DIR"

# v2 dashboard demo path: no Kafka, no bridge, no Docker. Clear legacy auth
# knobs that can conflict with the local no-login showcase.
unset KAFKA_BROKERS
unset WAYLOG_API_KEY

export GOCACHE="$GOCACHE_DIR"
export INGEST_ADDR="${INGEST_ADDR:-127.0.0.1:8080}"
export INGEST_URL="${INGEST_URL:-http://127.0.0.1:8080}"
export CORS_ORIGIN="${CORS_ORIGIN:-http://localhost:9081}"
export WAYLOG_WRITE_KEY="${WAYLOG_WRITE_KEY:-demo}"
export DASHBOARD_AUTH="${DASHBOARD_AUTH:-off}"
if [[ "$DASHBOARD_AUTH" == "off" ]]; then
  export WAYLOG_READ_KEY=""
else
  export WAYLOG_READ_KEY="${WAYLOG_READ_KEY:-demo}"
fi
export WAYLOG_V2_READS="${WAYLOG_V2_READS:-true}"
export EVENT_LOG_DIR="${EVENT_LOG_DIR:-${STATE_DIR}/eventlog}"
export EVENT_LOG_V2_DIR="${EVENT_LOG_V2_DIR:-${STATE_DIR}/eventlog-v2}"
export SNAPSHOT_PATH="${SNAPSHOT_PATH:-${STATE_DIR}/graph_snapshot.json}"
export SQLITE_PATH="${SQLITE_PATH:-${STATE_DIR}/waylog.db}"

start() {
  local name="$1"
  shift
  local log="${LOG_DIR}/${name}.log"
  nohup "$@" >"$log" 2>&1 &
  echo "$!" >"${STATE_DIR}/${name}.pid"
}

build_bin() {
  local name="$1"
  local pkg="$2"
  go build -o "${BIN_DIR}/${name}" "$pkg"
}

fail_service() {
  local name="$1"
  local target="$2"
  local log="${LOG_DIR}/${name}.log"
  echo "Timed out waiting for ${name} at ${target}" >&2
  echo "Log: ${log}" >&2
  if [[ -f "$log" ]]; then
    echo "" >&2
    echo "Last ${name} log lines:" >&2
    tail -40 "$log" >&2 || true
  fi
  exit 1
}

process_alive() {
  local name="$1"
  local pid_file="${STATE_DIR}/${name}.pid"
  [[ -f "$pid_file" ]] || return 1
  local pid
  pid="$(cat "$pid_file" 2>/dev/null || true)"
  [[ -n "$pid" ]] && kill -0 "$pid" >/dev/null 2>&1
}

wait_for_http() {
  local url="$1"
  local name="$2"
  for _ in $(seq 1 80); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    if ! process_alive "$name"; then
      fail_service "$name" "$url"
    fi
    sleep 0.25
  done
  fail_service "$name" "$url"
}

wait_for_tcp() {
  local host="$1"
  local port="$2"
  local name="$3"
  for _ in $(seq 1 80); do
    if (: >"/dev/tcp/${host}/${port}") >/dev/null 2>&1; then
      return 0
    fi
    if ! process_alive "$name"; then
      fail_service "$name" "${host}:${port}"
    fi
    sleep 0.25
  done
  fail_service "$name" "${host}:${port}"
}

echo "Building local demo binaries..."
pids=()
build_bin ingest ./cmd/ingest & pids+=($!)
build_bin api-gateway ./examples/cmd/api-gateway & pids+=($!)
build_bin checkout-demo ./examples/cmd/checkout-demo & pids+=($!)
build_bin payment-demo ./examples/cmd/payment-demo & pids+=($!)
build_bin db-demo ./examples/cmd/db-demo & pids+=($!)
for pid in "${pids[@]}"; do
  if ! wait "$pid"; then
    echo "Build failed (pid $pid)" >&2
    exit 1
  fi
done

start ingest "${BIN_DIR}/ingest"
wait_for_http "${INGEST_URL}/readyz" "ingest"

start db-demo "${BIN_DIR}/db-demo"
wait_for_tcp 127.0.0.1 9084 db-demo

start payment-demo "${BIN_DIR}/payment-demo"
wait_for_tcp 127.0.0.1 9083 payment-demo

start checkout-demo "${BIN_DIR}/checkout-demo"
wait_for_tcp 127.0.0.1 9082 checkout-demo

start api-gateway "${BIN_DIR}/api-gateway"
wait_for_http "http://localhost:9081/demo" "api-gateway"

cat <<INFO
Waylog dashboard demo is running.

Open:
  Demo controls: http://localhost:9081/demo
  Dashboard:     http://localhost:8080/ui/

How to demo it:
  1. Open Demo controls and click "Run traffic burst".
  2. Open Dashboard and inspect errors, impact, and trace explanation.
  3. Or run: make demo-acceptance

Useful CLI checks:
  ./waylog capabilities
  ./waylog recent --limit 5
  ./waylog errors --window 15m
  ./waylog blast --service checkout --step payment.charge --code PMT_502 --window 15m

Logs:
  ${LOG_DIR}

Stop:
  make demo-stop
INFO
