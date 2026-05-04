#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

STATE_DIR="${WAYLOG_DEMO_STATE_DIR:-./data/demo-state}"

if [[ -d "$STATE_DIR" ]]; then
  for pid_file in "$STATE_DIR"/*.pid; do
    [[ -e "$pid_file" ]] || continue
    pid="$(cat "$pid_file" 2>/dev/null || true)"
    if [[ -n "${pid:-}" ]]; then
      kill "$pid" >/dev/null 2>&1 || true
    fi
    rm -f "$pid_file"
  done
fi

./scripts/micro-demo-stop.sh >/dev/null 2>&1 || true

echo "Waylog demo stopped."
