#!/usr/bin/env bash
# Deterministic OTLP fixture checks for Waylog's HTTP and gRPC trace paths.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "[otlp-conformance] running OTLP conversion and receiver tests"
go test ./internal/otel/...
echo "OK: OTLP HTTP/gRPC fixture checks passed"
