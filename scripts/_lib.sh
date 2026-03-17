#!/usr/bin/env bash
# Shared helpers for demo scripts. Source this file, don't execute it.
# Usage: source "$(dirname "$0")/_lib.sh"

BASE_URL="${BASE_URL:-http://localhost:8080}"
_passed=0
_failed=0

check() {
  local desc="$1"
  local url="$2"
  local expected_status="$3"
  local status
  status=$(curl -s -o /dev/null -w "%{http_code}" "$url" || echo "000")
  if [[ "$status" == "$expected_status" ]]; then
    echo "PASS: $desc (HTTP $status)"
    _passed=$((_passed + 1))
  else
    echo "FAIL: $desc (expected HTTP $expected_status, got $status)"
    _failed=$((_failed + 1))
  fi
}

check_post() {
  local desc="$1"
  local url="$2"
  local body="$3"
  local expected_status="$4"
  local status
  status=$(curl -s -o /dev/null -w "%{http_code}" -X POST -H "Content-Type: application/json" -d "$body" "$url" || echo "000")
  if [[ "$status" == "$expected_status" ]]; then
    echo "PASS: $desc (HTTP $status)"
    _passed=$((_passed + 1))
  else
    echo "FAIL: $desc (expected HTTP $expected_status, got $status)"
    _failed=$((_failed + 1))
  fi
}

check_status() {
  local desc="$1" actual="$2" expected="$3"
  if [[ "$actual" == "$expected" ]]; then
    echo "  PASS: $desc (HTTP $actual)"
    _passed=$((_passed + 1))
  else
    echo "  FAIL: $desc (expected HTTP $expected, got $actual)"
    _failed=$((_failed + 1))
  fi
}

rand_hex() {
  local n="$1"
  LC_ALL=C tr -dc 'a-f0-9' </dev/urandom | head -c "$n"
}

pretty_json() {
  if command -v jq &>/dev/null; then
    jq .
  else
    cat
  fi
}

jq_extract() {
  local filter="$1"
  local input="$2"
  if command -v jq &>/dev/null; then
    echo "$input" | jq -r "$filter" 2>/dev/null || echo ""
  else
    local key
    key=$(echo "$filter" | grep -o '[a-z_]*' | tail -1)
    echo "$input" | grep -o "\"${key}\":[^,}]*" | head -1 | sed 's/.*://' | sed 's/[" ]//g'
  fi
}

wait_ready() {
  echo "Waiting for ${BASE_URL}/readyz ..."
  for i in $(seq 1 30); do
    local status
    status=$(curl -s -o /dev/null -w "%{http_code}" "${BASE_URL}/readyz" 2>/dev/null || echo "000")
    if [[ "$status" == "200" ]]; then
      echo "Stack ready."
      return
    fi
    if [[ $i -eq 30 ]]; then
      echo "ERROR: stack not ready after 30s"
      exit 1
    fi
    sleep 1
  done
}

print_results() {
  echo "=== Results: $_passed passed, $_failed failed ==="
  if [[ $_failed -gt 0 ]]; then
    exit 1
  fi
}
