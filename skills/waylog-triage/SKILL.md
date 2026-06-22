---
name: waylog-triage
description: Use when triaging or reviewing production incidents with Waylog — ranks the active-incident queue by impact, investigates an incident through Waylog's deterministic tools, and produces a cited brief the on-call engineer decides on. Read-only: it recommends, it never acts.
---

# Waylog incident triage

Waylog is a deterministic, self-hosted incident-triage platform. This skill drives
its tools to (1) surface what needs attention and (2) investigate a single
incident and hand the on-call engineer a cited brief. Provider-neutral: it refers
to "tools", not any one runtime.

## The one rule: you accelerate, the engineer decides

AI speeds up the investigation; **an on-call engineer makes the call.** Code in
production can have customer and financial blast radius — a brief is
*decision-support*, never an autonomous decision or action. Every brief or comment
this skill produces MUST begin with:

> *AI-assisted triage — evidence is tool-derived and reproducible; an on-call engineer owns the decision.*

Two more non-negotiables:
- **Ground every claim in tool output.** Never infer root cause, impact, or "what
  changed" from prior knowledge. Cite the `report_hash` and `evidence_fingerprint`.
- **Recommend, don't act.** Do not route, page, revert, or remediate. You may
  *draft* a Slack/ticket message; the engineer sends it.

## Connecting (prerequisites)

The tools must be reachable first:
- **Local agent** (Waylog on the same machine): MCP over **stdio** —
  `MCP_STDIO=1 ./ingest`. See [`../../docs/mcp.md`](../../docs/mcp.md).
- **Remote / running instance**: the REST tool surface, `POST /v1/tools/{name}`
  with an agent-scoped key (`WAYLOG_AGENT_KEY`); the queue (Workflow 1) also needs
  the read API `GET /v1/incidents/active` (read key).

Full tool schemas come from MCP `tools/list` or `docs/mcp.md` — not restated here.
Runnable examples: [`examples/triage.sh`](examples/triage.sh) (REST) and
[`examples/mcp-tools-call.jsonl`](examples/mcp-tools-call.jsonl) (stdio).

## What the engine already decided (don't relabel it)

Waylog's engine owns incident state — your job is to investigate on top of it, not
to change it:
- **status**: `active` · `recovering` · `resolved`
- **cause**: `deploy` · `dependency` · `runtime` · `app` · `unknown`
- **confidence**: `high` · `medium` · `low`
- **`report_hash`** — identical across surfaces *within one engine tick* (agreement key).
- **`evidence_fingerprint`** — stable *across ticks* until the evidence set changes
  (the durable citation; use it to recognize "same incident, same evidence as before").

## Invocation

The engineer asks in natural language; interpret and act. Examples:
- "What needs my attention?" → Workflow 1.
- "Triage inc_01HX…" / "What changed for inc_01HX…?" → Workflow 2.
- "Render inc_01HX… for Slack" → `render_triage_report` (draft; the engineer sends).

## Workflow 1 — What needs attention

Call `list_active_incidents` (`{env?, limit?}`). It already returns the queue
**ranked by impact** (affected users → requests → services) with a
`needs_judgment` flag set when `cause=unknown`, `confidence=low`, or there's no
correlated suspect change. Present a count and a one-line summary each, leading
with impact and pointing at the flagged judgment calls; let the engineer pick
which to triage. (Equivalent read-API path: `GET /v1/incidents/active`.)

## Workflow 2 — Triage one incident

1. **Gather (cited).** `triage_incident {incident_id, snapshot:true}` → impact,
   cause, confidence, `report_hash`, `evidence_fingerprint`. If a prior brief
   exists, compare its `evidence_fingerprint`: unchanged ⇒ say "evidence unchanged
   since last brief" and don't re-derive resolved points.
2. **What changed.** `suspect_change {incident_id}` → if a deploy correlated, name
   service/version/PR/author + the before/after error-rate delta as the leading
   hypothesis. If `NOT_FOUND`, state "no deploy correlated" — do **not** invent one.
3. **Verify / scope.** `blast_radius` for the top error family to quantify reach;
   `explain_request {trace_id}` on a sample trace to show the failing step (the
   deterministic stand-in for "reproduction").
4. **When the engine is unsure** (`cause=unknown`/`confidence=low`/thin evidence):
   surface the incident's `instrumentation_warnings` and say plainly *what is
   missing and what to instrument or register* (push deploy provenance, emit the
   dependency signal, add the missing span) so the next occurrence is diagnosable.
   There is no reporter to grill — the output is a system-improvement note.
5. **Brief + recommend (read-only).** Produce the cited brief per
   [`reference/AGENT-BRIEF.md`](reference/AGENT-BRIEF.md): hypothesis · suspect
   change · impact · citations (`report_hash`, `evidence_fingerprint`,
   trace/alert/signal ids) · recommended next action for the engineer. Optionally
   `render_triage_report {format:"slack"}` to draft a channel message — the
   engineer sends it.

## What NOT to do

- Don't decide or act — recommend; the engineer owns the call.
- Don't route, page, revert, or remediate (draft only).
- Don't fetch code, diffs, or live VCS data — provenance is only what CI pushed at
  deploy time.
- Don't upgrade uncertainty to certainty — report the engine's confidence as given.
- Don't invent fields or relabel engine state.
