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

# ─────────────────────────────────────────────────────────────
# Visual renderers for demo output
# ─────────────────────────────────────────────────────────────

# render_chain — reads /v1/traces/story JSON from stdin and prints an
# indented propagation chain with a first-failure marker. Falls back to
# raw JSON if jq is unavailable.
#
# Example input shape:
#   {"story":{"trace_id":"...","chain":[{hop},...],"first_fail_hop":{...}},
#    "context":{"flow":"purchase","user_tier":"premium","user_region":"us-east-1"}}
render_chain() {
  if ! command -v jq &>/dev/null; then
    cat
    return
  fi

  local raw
  raw=$(cat)

  local trace_id flow tier region ffid
  trace_id=$(echo "$raw" | jq -r '(.story // .).trace_id // ""')
  flow=$(echo "$raw"     | jq -r '(.context // {}).flow // ""')
  tier=$(echo "$raw"     | jq -r '(.context // {}).user_tier // ""')
  region=$(echo "$raw"   | jq -r '(.context // {}).user_region // ""')
  ffid=$(echo "$raw"     | jq -r '(.story // .).first_fail_hop.span_id // ""')

  if [[ -z "$trace_id" || "$trace_id" == "null" ]]; then
    echo "  (no trace story available)"
    return
  fi

  local short="${trace_id:0:8}"
  local header="trace ${short}…"
  [[ -n "$flow"   && "$flow"   != "null" ]] && header+="   flow=${flow}"
  [[ -n "$tier"   && "$tier"   != "null" ]] && header+="   user=${tier}"
  [[ -n "$region" && "$region" != "null" ]] && header+="   region=${region}"

  echo ""
  echo "  ${header}"
  echo ""

  # Per-hop TSV: idx<TAB>is_root<TAB>is_ffh<TAB>service<TAB>status<TAB>error<TAB>latency
  local hops
  hops=$(echo "$raw" | jq -r --arg ffid "$ffid" '
    (.story // .).chain // []
    | to_entries
    | .[]
    | [
        .key,
        (.value.is_root | tostring),
        ((.value.span_id == $ffid and $ffid != "") | tostring),
        .value.service,
        (.value.status_code | tostring),
        (.value.error_code // ""),
        (.value.latency_ms | tostring)
      ]
    | @tsv')

  while IFS=$'\t' read -r idx is_root is_ffh service status err latency; do
    [[ -z "$idx" ]] && continue

    local prefix
    if [[ "$idx" == "0" ]]; then
      prefix="  "
    else
      local pad="" d
      for ((d=1; d<idx; d++)); do pad+="    "; done
      prefix="  ${pad}    └─ "
    fi

    local tail=""
    if [[ "$is_root" == "true" ]]; then
      tail="   (root)"
    elif [[ "$is_ffh" == "true" ]]; then
      tail="   ← first failure"
    fi

    printf "%s%-20s %-4s  %-16s %5s ms%s\n" \
      "$prefix" "$service" "$status" "$err" "$latency" "$tail"
  done <<< "$hops"
}

# render_recent_deploy — calls GET /v1/deployments?service=X&window=Y and
# prints a one-line correlation summary for the most recent deploy. Falls
# back silently (no output) if cold store is not configured, the service
# has no recent deploys, or jq is unavailable.
#
# Usage: render_recent_deploy <service> [window]
render_recent_deploy() {
  local service="$1"
  local window="${2:-1h}"

  if ! command -v jq &>/dev/null; then
    return
  fi

  local raw
  raw=$(curl -s "${BASE_URL}/v1/deployments?service=${service}&window=${window}")

  # Server may have responded with an error (e.g. cold store unavailable)
  # or an empty list — in either case just skip.
  local line
  line=$(echo "$raw" | jq -r '
    (.data // .) as $r |
    ($r.deployments // [])
    | sort_by(.first_seen)
    | last
    | if . == null then empty
      else
        . as $d
        # fromdateiso8601 rejects fractional seconds; strip them first.
        | (.first_seen | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) as $fs
        | (now - $fs) as $elapsed
        | (if   $elapsed < 60    then "\(($elapsed|floor)) s"
             elif $elapsed < 3600 then "\(($elapsed/60|floor)) min"
             else                      "\(($elapsed/3600|floor)) h"
           end) as $ago
        | "  correlated:    \($d.service) \($d.version // "") deployed \($ago) ago"
      end
  ' 2>/dev/null || true)

  [[ -n "$line" ]] && echo "$line"
}

# render_blast_radius — calls GET /v1/blast_radius and prints a one-line
# impact summary. Handles both enveloped and raw responses.
#
# Usage: render_blast_radius <error_code> [window]
render_blast_radius() {
  local error_code="$1"
  local window="${2:-10m}"

  local raw
  raw=$(curl -s "${BASE_URL}/v1/blast_radius?error_code=${error_code}&window=${window}")

  if ! command -v jq &>/dev/null; then
    echo "  $raw"
    return
  fi

  local data reqs users svc_count
  data=$(echo "$raw" | jq -c '.data // .')
  reqs=$(echo   "$data" | jq -r '.affected_requests // 0')
  users=$(echo  "$data" | jq -r '.affected_users // 0')
  svc_count=$(echo "$data" | jq -r '(.services // []) | length')

  printf "  blast radius:  %s requests · %s users · %s services\n" \
    "$reqs" "$users" "$svc_count"
}

# rule — print a horizontal divider for section headers.
rule() {
  local char="${1:-─}"
  printf '%.0s'"$char" {1..60}
  echo ""
}

# banner — print a double-rule bordered title.
banner() {
  local title="$1"
  echo ""
  rule "━"
  printf "  %s\n" "$title"
  rule "━"
}

# section — print a single-rule section header.
section() {
  local title="$1"
  echo ""
  rule "─"
  printf "  %s\n" "$title"
  rule "─"
}
