#!/usr/bin/env bash
set -euo pipefail

GOCACHE_DIR="${GOCACHE:-/tmp/go-build}"
export GOCACHE="$GOCACHE_DIR"

# v2 demo path: Kafka and cmd/bridge are intentionally unused here.
unset KAFKA_BROKERS

export INGEST_ADDR="${INGEST_ADDR:-:8080}"
export INGEST_URL="${INGEST_URL:-http://localhost:8080}"
export WAYLOG_WRITE_KEY="${WAYLOG_WRITE_KEY:-demo}"
export WAYLOG_READ_KEY="${WAYLOG_READ_KEY:-demo}"
export DASHBOARD_AUTH="${DASHBOARD_AUTH:-key:demo}"
export WAYLOG_V2_READS="${WAYLOG_V2_READS:-true}"
export EVENT_LOG_V2_DIR="${EVENT_LOG_V2_DIR:-./data/eventlog-v2-demo}"

pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT
trap 'exit 0' INT

wait_for_http() {
  local url="$1"
  local name="$2"
  for _ in $(seq 1 60); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  echo "Timed out waiting for ${name} at ${url}" >&2
  exit 1
}

GOCACHE="$GOCACHE_DIR" go run ./cmd/ingest 2> ingest.log &
pids+=("$!")

wait_for_http "${INGEST_URL}/readyz" "ingest"

GOCACHE="$GOCACHE_DIR" go run ./examples/cmd/db-demo &
pids+=("$!")

GOCACHE="$GOCACHE_DIR" go run ./examples/cmd/payment-demo &
pids+=("$!")

GOCACHE="$GOCACHE_DIR" go run ./examples/cmd/checkout-demo &
pids+=("$!")

GOCACHE="$GOCACHE_DIR" go run ./examples/cmd/api-gateway &
pids+=("$!")

wait_for_http "http://localhost:9081/demo" "api-gateway"

cat <<INFO

Schema-2.0 micro-demo running.
- Gateway UI:  http://localhost:9081/demo
- Gateway API: http://localhost:9081/purchase
- Ingest API:  ${INGEST_URL}

Trigger the main incident:
  curl -s -X POST http://localhost:9081/purchase \\
    -H 'Content-Type: application/json' \\
    --data '{"sku":"X1","scenario":"payment_502"}'

Investigate with the v2 CLI:
  WAYLOG_READ_KEY=${WAYLOG_READ_KEY} ./waylog errors --window 15m
  WAYLOG_READ_KEY=${WAYLOG_READ_KEY} ./waylog explain <trace_id>
  WAYLOG_READ_KEY=${WAYLOG_READ_KEY} ./waylog blast --service checkout --step payment.charge --code PMT_502 --window 15m
  WAYLOG_READ_KEY=${WAYLOG_READ_KEY} ./waylog blast --code PMT_502 --window 15m

Press Ctrl+C to stop everything.
INFO

wait
