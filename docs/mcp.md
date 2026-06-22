# Connecting an MCP client to Waylog

Waylog exposes its deterministic triage tools over the Model Context Protocol
(MCP) so any agent — Claude, Cursor, or a custom client — can drive incident
triage with no Waylog-specific glue. Two transports share one protocol core, so
they behave identically:

- **stdio** — the client launches `ingest` as a local subprocess. Best for a
  *solo/dev* setup where Waylog runs on the same machine.
- **Streamable HTTP** — the client connects over the network to a *running*
  Waylog. This is how a **team's** agents reach the shared server that holds the
  real incidents (stdio would spawn a separate, empty process).

## Transport 1 — stdio (local)

```bash
MCP_STDIO=1 SQLITE_PATH=/var/lib/waylog/waylog.db ./bin/ingest
```

| Env var | Purpose |
|---------|---------|
| `MCP_STDIO=1` | Serve MCP over stdio instead of the REPL |
| `SQLITE_PATH` | Cold store path (incidents, deployments, signals) |

## Transport 2 — Streamable HTTP (team / remote)

Enable the endpoint on the normal HTTP server:

```bash
MCP_HTTP=1 SQLITE_PATH=/var/lib/waylog/waylog.db \
  WAYLOG_AGENT_KEY=your-agent-key ./bin/ingest
```

`POST /mcp` then speaks MCP JSON-RPC, behind the same agent-scope auth and rate
limit as `/v1/tools`. It is **stateless** (no session bookkeeping), so any agent
can hit any replica of a shared server.

```jsonc
// Remote MCP client config — point it at the server's internal address.
{
  "mcpServers": {
    "waylog": {
      "url": "http://waylog.internal:8080/mcp",
      "headers": { "Authorization": "Bearer your-agent-key" }
    }
  }
}
```

**No public domain required.** Address the server however you reach internal
services — k8s service DNS (`http://waylog.monitoring.svc:8080/mcp`), an internal
hostname, or `IP:port`. If the endpoint leaves your private network, terminate
**TLS at your ingress / reverse proxy** (that's also where a cert/domain would
live, if at all) — Waylog itself serves plain HTTP and relies on the agent key
for access control.

| Env var | Purpose |
|---------|---------|
| `MCP_HTTP=1` | Serve MCP over Streamable HTTP at `POST /mcp` |
| `WAYLOG_AGENT_KEY` | Required agent auth for `/mcp` (same scope as `/v1/tools`) |
| `SQLITE_PATH` | Cold store path (incidents, deployments, signals) |

## Tools advertised

`tools/list` returns the name, description, and input/output JSON schema for each
tool, so a client can self-discover without hard-coding anything:

| Tool | Input | Returns |
|------|-------|---------|
| `list_active_incidents` | `{env?, limit?}` | Active incidents ranked by impact, `needs_judgment`-flagged (the on-call queue) |
| `explain_request` | `{trace_id}` | Trace story (path, anchor, downstream) |
| `blast_radius` | `{service, step, error_code, window?}` | Affected requests/users/services |
| `triage_incident` | `{incident_id, window?, snapshot?}` | Deterministic `TriageReport` |
| `render_triage_report` | `{incident_id, format?, window?, snapshot?}` | Rendered report (markdown/slack/pagerduty) |
| `suspect_change` | `{incident_id, window?, snapshot?}` | Correlated deploy/PR + before/after error-rate delta |

All results are deterministic: `triage_incident` carries a reproducible
`report_hash`, and `suspect_change` reflects the deploy the incident classifier
already correlated. No tool makes external network calls at query time.

## Generic client config

The server is the same regardless of host; only the launcher config differs.

```jsonc
// Generic MCP client config
{
  "mcpServers": {
    "waylog": {
      "command": "/path/to/bin/ingest",
      "env": { "MCP_STDIO": "1", "SQLITE_PATH": "/var/lib/waylog/waylog.db" }
    }
  }
}
```

## Example call

```jsonc
{"jsonrpc":"2.0","id":1,"method":"tools/list"}
{"jsonrpc":"2.0","id":2,"method":"tools/call",
 "params":{"name":"suspect_change","arguments":{"incident_id":"inc_01HX..."}}}
```

See [`skills/waylog-triage/`](../skills/waylog-triage/) for a portable skill that
teaches an agent the end-to-end triage workflow over these tools.
