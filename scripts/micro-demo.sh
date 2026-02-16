#!/usr/bin/env bash
set -euo pipefail

GOCACHE_DIR="${GOCACHE:-/tmp/go-build}"
export GOCACHE="$GOCACHE_DIR"

export KAFKA_BROKERS="${KAFKA_BROKERS:-localhost:9092}"
export KAFKA_TOPIC="${KAFKA_TOPIC:-wide_events}"
export INGEST_ADDR="${INGEST_ADDR:-:8080}"

pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done

  if [[ "${START_KAFKA:-}" == "1" ]]; then
    docker compose -f docker-compose.kafka.yml down -v >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

handle_interrupt() {
  exit 0
}
trap handle_interrupt INT

if [[ "${START_KAFKA:-}" == "1" ]]; then
  docker compose -f docker-compose.kafka.yml up -d
fi

# Start payment-demo
GOCACHE="$GOCACHE_DIR" go run ./cmd/payment-demo &
pids+=("$!")

# Start checkout-demo
GOCACHE="$GOCACHE_DIR" go run ./cmd/checkout-demo &
pids+=("$!")

# Start api-gateway
GOCACHE="$GOCACHE_DIR" go run ./cmd/api-gateway &
pids+=("$!")

# Start Kafka -> ingest bridge
GOCACHE="$GOCACHE_DIR" go run ./cmd/bridge &
pids+=("$!")

cat <<INFO

Micro-demo running (3-service chain: gateway -> checkout -> payment).
- Gateway UI:  http://localhost:9081/demo
- Gateway API: http://localhost:9081/purchase

Use the UI to send requests, then query with:
  waylog "show top errors"
  waylog "trace summary for trace <trace-id>"

To launch the TUI dashboard:
  make waylog-live

Press Ctrl+C to stop everything.
INFO

# Run ingest in foreground
GOCACHE="$GOCACHE_DIR" go run ./cmd/ingest 2> ingest.log
