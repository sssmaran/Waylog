# Connecting an MCP client to Waylog

Waylog exposes its deterministic triage tools over the Model Context Protocol
(MCP) so any agent — Claude, Cursor, or a custom client — can drive incident
triage with no Waylog-specific glue. The MCP server speaks JSON-RPC 2.0 over
stdio: the client launches `ingest` as a local subprocess.

## Run the server

```bash
MCP_STDIO=1 SQLITE_PATH=/var/lib/waylog/waylog.db ./bin/ingest
```

| Env var | Purpose |
|---------|---------|
| `MCP_STDIO=1` | Serve MCP over stdio instead of the REPL |
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
