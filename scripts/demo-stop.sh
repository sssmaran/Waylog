#!/usr/bin/env bash
set -euo pipefail

# Stop Kafka (if running via compose)
docker compose -f docker-compose.kafka.yml down -v >/dev/null 2>&1 || true

# Kill any demo processes still running
pkill -f "go run ./cmd/checkout" >/dev/null 2>&1 || true
pkill -f "go run ./cmd/bridge" >/dev/null 2>&1 || true
pkill -f "go run ./cmd/ingest" >/dev/null 2>&1 || true
pkill -f "curl -s http://localhost:9090/checkout" >/dev/null 2>&1 || true
