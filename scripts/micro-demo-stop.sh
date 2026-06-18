#!/usr/bin/env bash
set -euo pipefail

# Kill v2 micro-demo processes.
pkill -f "go run ./examples/cmd/api-gateway" >/dev/null 2>&1 || true
pkill -f "go run ./examples/cmd/checkout-demo" >/dev/null 2>&1 || true
pkill -f "go run ./examples/cmd/payment-demo" >/dev/null 2>&1 || true
pkill -f "go run ./examples/cmd/db-demo" >/dev/null 2>&1 || true
pkill -f "go run ./cmd/ingest" >/dev/null 2>&1 || true
