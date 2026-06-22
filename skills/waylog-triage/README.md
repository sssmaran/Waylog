# waylog-triage skill

A portable Agent Skill that drives **deterministic, human-in-the-loop incident
triage** with Waylog's tools. It ranks the active-incident queue by impact,
investigates a single incident, and hands the on-call engineer a cited brief —
**read-only: it recommends, it never acts.**

```
waylog-triage/
  SKILL.md                  the workflow (loaded on demand)
  reference/AGENT-BRIEF.md  how to write the durable, cited brief
  examples/triage.sh        runnable end-to-end triage over REST
  examples/mcp-tools-call.jsonl  the stdio handshake → tool calls
```

## Install

- **Skill-aware agents (Claude, etc.):** drop this directory into the agent's
  skills path, or — once this repo is published — `npx skills add
  https://github.com/sssmaran/WaylogCLI --skill waylog-triage`.
- **Cursor / rules-based agents:** copy `SKILL.md`'s body into a project rule.
- **Custom agents:** include `SKILL.md` as a system message.

The skill loads its body on demand; the `reference/` and `examples/` files are
pulled in only when the workflow needs them (progressive disclosure).

## Connect the tools

The skill assumes Waylog's tools are reachable:
- **Local:** MCP over stdio (`MCP_STDIO=1 ./ingest`) — see
  [`../../docs/mcp.md`](../../docs/mcp.md).
- **Remote:** the REST tool surface (`POST /v1/tools/{name}`, agent-scoped key);
  the queue view also uses the read API `GET /v1/incidents/active`.

## Why it's safe to trust

Waylog's triage output carries a reproducible `report_hash` and a cross-tick
`evidence_fingerprint`; `suspect_change` reflects the deploy the engine already
correlated — no live VCS calls, no probabilistic root-cause guessing. The skill
enforces grounding every claim in tool output, and bakes in the rule that the
**on-call engineer makes the call** — the brief accelerates the investigation, it
doesn't replace the human who is responsible for what ships.
