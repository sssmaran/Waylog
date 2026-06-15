#!/bin/sh
# Rate-limit smoke: boots a throwaway ingest with a 5 rps write limit, floods
# /v1/events, and verifies 429 + Retry-After, per-key isolation, and recovery.
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PORT="${RATELIMIT_SMOKE_PORT:-18099}"
ADDR="127.0.0.1:$PORT"
TMP="$(mktemp -d)"
SRV_PID=""
cleanup() {
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

go build -o "$TMP/ingest" ./cmd/ingest

WAYLOG_RATE_LIMIT_WRITE_RPS=5 \
INGEST_ADDR="$ADDR" \
WAYLOG_WRITE_KEY=loadkey \
EVENT_LOG_DIR="$TMP/eventlog" \
EVENT_LOG_SYNC=false \
"$TMP/ingest" >"$TMP/ingest.log" 2>&1 &
SRV_PID=$!

ready=0
i=0
while [ "$i" -lt 50 ]; do
  if curl -fsS "http://$ADDR/readyz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.2
  i=$((i + 1))
done
[ "$ready" = 1 ] || { echo "FAIL: ingest did not become ready"; cat "$TMP/ingest.log"; exit 1; }

post() { # post <key> -> prints http status code
  curl -s -o /dev/null -w '%{http_code}' -X POST "http://$ADDR/v1/events" \
    -H "Authorization: Bearer $1" -H 'Content-Type: application/json' -d '{}'
}

# Flood: 20 rapid requests against a 5 rps / burst-5 budget.
codes=""
i=0
while [ "$i" -lt 20 ]; do
  codes="$codes $(post loadkey)"
  i=$((i + 1))
done
n429=$(echo "$codes" | tr ' ' '\n' | grep -c '^429$' || true)
[ "$n429" -ge 5 ] || { echo "FAIL: expected >=5 throttled requests, codes:$codes"; exit 1; }
echo "PASS: flood throttled ($n429/20 requests got 429)"

# A throttled response must carry Retry-After: 1.
retry_after=$(curl -s -D - -o /dev/null -X POST "http://$ADDR/v1/events" \
  -H 'Authorization: Bearer loadkey' -d '{}' | tr -d '\r' | grep -i '^retry-after:' | awk '{print $2}')
[ "$retry_after" = "1" ] || { echo "FAIL: Retry-After header missing on 429 (got '$retry_after')"; exit 1; }
echo "PASS: 429 carries Retry-After: 1"

# Per-key isolation: a different presented key must not be throttled
# (it fails auth with 401, never 429).
other=$(post otherkey)
[ "$other" != "429" ] || { echo "FAIL: other key was throttled by loadkey's bucket"; exit 1; }
echo "PASS: per-key isolation (other key got $other, not 429)"

# Recovery: after >1s the bucket refills and requests pass again.
sleep 1.5
recovered=$(post loadkey)
[ "$recovered" != "429" ] || { echo "FAIL: limiter did not recover after refill window"; exit 1; }
echo "PASS: clean recovery after refill (got $recovered)"

echo "ratelimit-smoke: all checks passed"
