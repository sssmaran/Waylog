#!/usr/bin/env bash
set -euo pipefail

GOCACHE_DIR="${GOCACHE:-/tmp/go-build}"
export GOCACHE="$GOCACHE_DIR"

export KAFKA_BROKERS="${KAFKA_BROKERS:-localhost:9092}"
export KAFKA_TOPIC="${KAFKA_TOPIC:-wide_events}"
export INGEST_ADDR="${INGEST_ADDR:-:8080}"
export INGEST_URL="${INGEST_URL:-http://localhost:8080/v1/events}"

CHECKOUT_URL="${CHECKOUT_URL:-http://localhost:9090/checkout}"
TRAFFIC_INTERVAL_SEC="${TRAFFIC_INTERVAL_SEC:-0.5}"

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

# Start checkout service (SDK emits to Kafka)
GOCACHE="$GOCACHE_DIR" go run ./cmd/checkout &
pids+=("$!")

# Start Kafka -> ingest bridge
GOCACHE="$GOCACHE_DIR" go run ./cmd/bridge &
pids+=("$!")

# Generate traffic
(
  while true; do
    curl -s "$CHECKOUT_URL" >/dev/null || true
    sleep "$TRAFFIC_INTERVAL_SEC"
  done
) &
pids+=("$!")

cat <<INFO

Demo running.
- Ingest will start in the foreground (logs + REPL).
- You can type questions like:
  waylog "show top errors"
  waylog "trace summary for trace <trace-id>"
  waylog "explain request <trace-id>"

Press Ctrl+C to stop everything.
INFO

# Run ingest in foreground so you can use the REPL for Gemini/tool registry.
GOCACHE="$GOCACHE_DIR" go run ./cmd/ingest 2> ingest.log
