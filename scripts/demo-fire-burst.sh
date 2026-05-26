#!/usr/bin/env bash
set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:9081}"
INGEST_URL="${INGEST_URL:-http://localhost:8080}"
WAYLOG_WRITE_KEY="${WAYLOG_WRITE_KEY:-demo}"
REQUESTS="${REQUESTS:-20}"
CONCURRENCY="${CONCURRENCY:-5}"
ALERT_TIMESTAMP="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

curl -fsS -X POST "${GATEWAY_URL}/demo/burst" \
  -H "Content-Type: application/json" \
  --data "{\"requests\":${REQUESTS},\"concurrency\":${CONCURRENCY}}" \
  >/dev/null

curl -fsS -X POST "${INGEST_URL}/v1/alerts" \
  -H "Authorization: Bearer ${WAYLOG_WRITE_KEY}" \
  -H "Content-Type: application/json" \
  --data "{\"receiver\":\"crux-demo\",\"status\":\"firing\",\"alerts\":[{\"status\":\"firing\",\"labels\":{\"alertname\":\"CheckoutPaymentFailure\",\"service\":\"checkout\",\"step\":\"payment.charge\",\"env\":\"demo\",\"severity\":\"critical\",\"error_code\":\"PMT_502\"},\"annotations\":{\"summary\":\"PMT_502 spike in checkout payment flow\",\"description\":\"Synthetic Crux demo alert for checkout payment failures\",\"runbook_url\":\"http://localhost:9081/demo\"},\"startsAt\":\"${ALERT_TIMESTAMP}\",\"generatorURL\":\"http://localhost:9081/demo\"}],\"commonLabels\":{\"alertname\":\"CheckoutPaymentFailure\",\"service\":\"checkout\",\"env\":\"demo\",\"severity\":\"critical\",\"error_code\":\"PMT_502\"},\"commonAnnotations\":{\"summary\":\"PMT_502 spike in checkout payment flow\"}}" \
  >/dev/null

echo "burst fired: demo deploy/dependency signals + alert + ${REQUESTS} payment_502 requests (${CONCURRENCY} concurrency)"
