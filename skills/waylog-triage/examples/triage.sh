#!/bin/sh
# Runnable end-to-end triage over Waylog's REST tool surface — the same workflow
# SKILL.md describes, as a working example an agent (or human) can run or copy.
#
# Prereqs: a running Waylog (`MCP_HTTP` not required — this uses REST /v1/tools),
# curl, and jq. Set WAYLOG_AGENT_KEY for the agent-scoped tool endpoints.
#
#   WAYLOG_URL=http://localhost:8080 \
#   WAYLOG_AGENT_KEY=your-agent-key \
#   WAYLOG_READ_KEY=your-read-key \
#   sh triage.sh
set -eu

URL="${WAYLOG_URL:-http://localhost:8080}"
AGENT="${WAYLOG_AGENT_KEY:-}"
READ="${WAYLOG_READ_KEY:-}"

command -v jq >/dev/null 2>&1 || { echo "jq is required"; exit 1; }
[ -n "$AGENT" ] || { echo "set WAYLOG_AGENT_KEY (agent-scoped tool calls)"; exit 1; }

read_get()  { curl -fsS -H "Authorization: Bearer ${READ}" "$URL$1"; }
tool_call() { curl -fsS -H "Authorization: Bearer ${AGENT}" -H 'Content-Type: application/json' \
                -d "$2" "$URL/v1/tools/$1"; }

# 1. Pick the most recent active incident.
inc="$(read_get /v1/incidents/active | jq -r '.incidents[0].incident_id // empty')"
[ -n "$inc" ] || { echo "no active incident"; exit 0; }
echo "incident: $inc"

# 2. Deterministic triage report (note the reproducible report_hash).
echo "--- triage_incident ---"
tool_call triage_incident "{\"incident_id\":\"$inc\"}" \
  | jq '{confidence, report_hash, evidence_fingerprint, impact: .blast_snapshot}'

# 3. What changed — the correlated deploy/PR (or NOT_FOUND).
echo "--- suspect_change ---"
tool_call suspect_change "{\"incident_id\":\"$inc\"}" 2>/dev/null \
  | jq '{deploy_id, service, commit_sha, pr_url, commit_author}' \
  || echo "(no deploy correlated)"

# 4. Render an operator report (markdown | slack | pagerduty).
echo "--- render_triage_report (markdown) ---"
tool_call render_triage_report "{\"incident_id\":\"$inc\",\"format\":\"markdown\"}" | jq -r '.body'
