#!/usr/bin/env bash
set -euo pipefail

# Stop Kafka (if running via compose)
docker compose -f docker-compose.kafka.yml down -v >/dev/null 2>&1 || true

# Kill micro-demo processes
pkill -f "go run ./examples/cmd/api-gateway" >/dev/null 2>&1 || true
pkill -f "go run ./examples/cmd/checkout-demo" >/dev/null 2>&1 || true
pkill -f "go run ./examples/cmd/payment-demo" >/dev/null 2>&1 || true
pkill -f "go run ./examples/cmd/db-demo" >/dev/null 2>&1 || true
pkill -f "go run ./cmd/bridge" >/dev/null 2>&1 || true
pkill -f "go run ./cmd/ingest" >/dev/null 2>&1 || true
pkill -f "go run ./cmd/waylog-live" >/dev/null 2>&1 || true
