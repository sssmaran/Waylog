<div align="center"><pre><img width="461" height="129" alt="Screenshot 2026-03-17 at 2 36 01 PM" src="https://github.com/user-attachments/assets/fdec0a1b-055a-47e3-b52c-cf1cf80fe373" />
</pre></div>

<p align="center">
  <strong>Structured logging that explains failed requests and active incidents.</strong><br>
  Drop-in SDKs (Go, TypeScript) or OTLP/HTTP. Agent-native by design.
</p>

<p align="center">
  <code>polyglot SDKs</code> · <code>agent-native API</code> · <code>failure tree</code> · <code>rollup-correct root cause</code>
</p>

<p align="center">
  <em>Public alpha — request triage plus signal-driven incident triage for backend systems.</em>
</p>

---

## What Waylog does

A request hits your API gateway, fans out to three services, and one of them fails. The gateway returns 502. Your logs say "upstream error." Waylog tells you exactly what happened in the request, then groups repeated failures into an incident with signal-backed cause evidence:

```text
  trace 7f3a2b9c…   flow=purchase   user=standard   region=us-east-1

  api-gateway        502   GW_DOWNSTREAM         14 ms   (root)
      └─ checkout    502   CHK_DOWNSTREAM         9 ms
          └─ db      200   —                      3 ms
          └─ payment 502   PMT_502                5 ms   ← first failure

  blast radius:  12 requests · 8 users · 4 services
```

This is not log search, metrics storage, or incident management. Waylog builds request-triage views from WideEvents, accepts production-context signals such as deploys and dependency health, and returns deterministic answers for "why did this trace fail?", "what incident is active?", and "who is affected by `PMT_502`?". Root-cause rollups count the originating failure once, not once per propagated hop.

Run `make demo` and see it yourself.

## Quick start

```bash
make demo
```

This starts the ingest server plus four real Go demo services wired through the schema-2.0 Go SDK (`api-gateway → checkout → db/payment`), enables `WAYLOG_V2_READS=true`, stores demo signals/incidents in local SQLite, and does not require Docker, Kafka, or the bridge process.

Once the stack is up:

1. Open demo controls at <http://localhost:9081/demo>, or open the dashboard at <http://localhost:8080/ui/>. The local demo disables dashboard login.
2. Click **Run traffic burst** to post demo deploy/dependency signals and fire a production-like mix through the checkout chain. For a focused single-trace look, click **Run payment outage** instead, or run:
   ```bash
   curl -s -X POST http://localhost:9081/purchase \
     -H 'Content-Type: application/json' \
     --data '{"sku":"X1","scenario":"payment_502"}'
   ```
3. Investigate with the v2 CLI:
   ```bash
   ./waylog incidents
   ./waylog incident <incident_id> --snapshot
   ./waylog errors --window 15m
   ./waylog explain <trace_id>
   ./waylog blast --service checkout --step payment.charge --code PMT_502 --window 15m
   ./waylog blast --code PMT_502 --window 15m
   ./waylog triage <incident_id>
   ```

The traffic burst posts fresh demo deploy/dependency signals on each run so the incident panel has evidence to attach. The demo also supports `happy` and `suppressed_payment_502` scenarios through the UI or `POST /purchase`.

Stop with `make demo-stop`.

Prefer Docker? Use `make docker-dev` / `make docker-down`. Prefer foreground service logs while hacking on Go code? Use `make micro-demo` and stop with `make micro-demo-stop`.


## How it works

1. **Capture** — services emit [WideEvents](docs/waylog-sdk-contract.md) via the Go or TypeScript SDK, or push OpenTelemetry spans to `/v1/otlp/v1/traces`. Every event is durably logged (WAL + fsync) before it enters the derived read models.
2. **Signal** — deploy systems, dependency monitors, or operators post small production-context facts to `/v1/signals`.
3. **Triage** — the ingest server projects request views (`recent`, `errors`, `explain`, `blast`) and opens incidents when error families spike against overlapping signals.
4. **Operator** — CLI, REST, MCP, TUI, and the embedded dashboard query the same derived views. Primary incident surfaces are `waylog incidents`, `waylog incident <id>`, `/v1/incidents/*`, and the dashboard incident cards.

## Get traces in

All three paths feed the same schema-2.0 ingest and read APIs. Pick whichever matches your stack.

### TypeScript SDK

```bash
npm install @waylog/sdk
```

```ts
import { waylog, useLogger } from "@waylog/sdk/express";

app.use(
  waylog({
    service: "checkout",
    env: "prod",
    ingestUrl: "http://localhost:8080",
    apiKey: process.env.WAYLOG_WRITE_KEY,
  }),
);

app.post("/buy", (req, res) => {
  useLogger(req).info("cart loaded", { user_id: req.user.id, tier: "vip" });
  res.sendStatus(200);
});
```

`@waylog/sdk` is ESM-only, Node 18+, and ships standalone core APIs plus Express, Hono, Next.js, and NestJS entrypoints (`@waylog/sdk/express`, `@waylog/sdk/hono`, `@waylog/sdk/next`, `@waylog/sdk/nest`).

### Go SDK

```go
import (
    "context"
    "net/http"

    waylog "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
    wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
)

func main() {
    _ = waylog.Init(waylog.Config{
        Service:   "checkout",
        Env:       "prod",
        Version:   "1.2.3",
        IngestURL: "http://localhost:8080",
    })
    defer waylog.Shutdown(context.Background())

    http.Handle("/", wayloghttp.HTTP(yourHandler))
}
```

The recommended SDK path is framework middleware plus `waylog.From(ctx)` / `useLogger(...)` inside handlers. Low-level request APIs such as `Begin`, `Finalize`, and `setField` are for adapter authors, tests, and unusual custom integrations. Full copy-paste examples for `net/http`, chi, gin, echo, standalone TypeScript, Express, Hono, Next.js, and NestJS are in [`docs/sdk-examples.md`](docs/sdk-examples.md).

### OTLP/HTTP traces

Point your existing OpenTelemetry collector at `http://localhost:8080/v1/otlp/v1/traces`. Protobuf bodies are accepted (gzip optional) and HTTP spans convert to schema-2.0 WideEvents on the way in, then show up in the same errors, explain, blast, and recent-trace APIs as SDK events when `WAYLOG_V2_READS=true`. **Phase A covers traces over HTTP.** gRPC, logs, and metrics are not yet shipping.

### Alternative: local ingest server (no Docker)

```bash
make ingest
```

Runs only the ingest server. Point your own services at it via an SDK or OTLP. Full env-var reference: [`docs/env.md`](docs/env.md).

## What you can ask

### CLI

```bash
WAYLOG_V2_READS=true ./ingest

waylog capabilities
waylog recent --limit 5
waylog errors --window 15m
waylog blast checkout:payment.charge:PMT_502 --window 15m
waylog explain trace_01HX...
waylog trace trace_01HX...
waylog event event_01HX...
waylog search PMT_502 --window 1h
```

The `waylog` binary is now the v2 operator CLI over the running ingest server's read APIs. Most verbs require the server to advertise `v2_reads.enabled=true` from `/v1/capabilities`; `waylog capabilities` is intentionally ungated so it can diagnose server setup. The CLI uses `INGEST_ADDR`, `WAYLOG_READ_KEY`, and `WAYLOG_CLI_TIMEOUT` by default. Add `--json` to any verb for machine-readable output.

### REST (direct tool call)

```bash
curl -X POST http://localhost:8080/v1/tools/blast_radius \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $WAYLOG_AGENT_KEY" \
  -d '{"error_code":"PMT_502","window":"10m","include_services":true}'
```

### REST (multi-step plan)

```bash
curl -X POST http://localhost:8080/v1/plans/execute \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $WAYLOG_AGENT_KEY" \
  -d '{
    "steps": [
      {"id":"patterns", "tool":"failure_patterns", "params":{"window":"10m"}},
      {"id":"blast",    "tool":"blast_radius",     "params":{"error_code":"PMT_502","window":"10m"}}
    ]
  }'
```

Plans execute deterministically server-side with SSE progress on `/v1/stream/plans/{id}`.

### Trace story

```bash
curl "http://localhost:8080/v1/traces/story?trace_id=$TRACE" \
  -H "Authorization: Bearer $WAYLOG_READ_KEY"
```

Returns the first failing step, contributing path, logs, downstream calls, and linkage mode used by the dashboard and `waylog explain`.

### MCP (agent surface)

```bash
make ingest-mcp    # MCP_STDIO=1
```

Exposes the same tool registry over MCP stdio for Claude, Cursor, and other MCP clients. Same semantics as the REST API.

### Analysis tools

All eleven tools are deterministic, idempotent, and available via CLI, REST `/v1/tools/{name}`, MCP, and plan execution.

| Tool               | Answers                                                       |
| ------------------ | ------------------------------------------------------------- |
| `graph_stats`      | Overall shape of the graph right now                          |
| `explain_request`  | Why did this specific trace fail?                             |
| `trace_summary`    | Span tree and timing for a trace                              |
| `graph_failures`   | Which requests are currently failing?                         |
| `failure_patterns` | What error codes dominate this window?                        |
| `blast_radius`     | How many requests, users, and services does this error touch? |
| `failure_chain`    | How did this failure propagate through services?              |
| `graph_query`      | DSL query over the graph (`expr` + `window`)                  |
| `compare_windows`  | Diff error rates between two windows                          |
| `graph_insights`   | Windowed rollup of top errors and patterns                    |
| `triage_incident`  | One structured TriageReport for an open incident (blast + first failure + signals + next checks) |

Full schemas: `GET /v1/tools` or [`docs/openapi.yaml`](docs/openapi.yaml).

## Dashboard

The embedded dashboard at `/ui` is a v2 triage surface over the same read APIs as the CLI. It requires `WAYLOG_V2_READS=true` and uses the dashboard session cookie for read-scope auth.

- dark, minimal Geist UI with aligned KPI modules and inline SVG mini-graphs
- `#/errors` — top error families over `/v1/errors`
- `#/explain/<id>` — first observable failing step over `/v1/traces/story`
- `#/blast/<key>` — impact panel over `/v1/blast_radius`
- `#/incident/<id>` — incident evidence and next checks over `/v1/incidents/{id}`
- recent-request stream from `/v1/traces/recent`, polled every 5s
- no Chart.js, Cytoscape, topology-first UI, Ask panel, deploy diff, or large dashboard charts


## Architecture

```text
Go / TS services (SDK) · OTLP/HTTP collectors
        │  schema-2.0 WideEvents · OTLP/HTTP traces
        ▼
  ingest server
    ├─ event log (append-only WAL, source of truth)
    ├─ derived read models (errors · explain · blast · recent traces · incidents)
    ├─ SQLite cold store (events · deployments · signals · incidents · causal claims)
    ├─ tool registry · Ask · plan execution
    └─ v2 dashboard · health · metrics · OpenAPI
        │
        ├──▶ /ui dashboard (Geist, no vendored chart/topology libs)
        ├──▶ /v1/tools/* · /v1/plans/execute (agent-native)
        └──▶ CLI · TUI · MCP · agents
```

Events are durably logged before projection — if the process crashes, replay rebuilds the read models from the WAL on next boot.

Durability model, retention, merge semantics, readiness policy, and counter buffer: [`docs/internals.md`](docs/internals.md). Full v2 HTTP contract: [`docs/openapi.yaml`](docs/openapi.yaml).

## Development

```bash
make build          # core binaries
make build-examples # demo services
make fmt vet test   # checks
make test-race      # race detector
make ts-test        # TypeScript SDK vitest suite
make ci             # fmt + vet + test-race + test-sdk + ts-test + doc-link + rollup-contract
make demo-acceptance # with make demo running, verify demo + CLI triage loop
```

## Auth

Waylog uses three scoped keys. They are independent — the dashboard never holds the agent key.

| Key                | Protects                                              |
| ------------------ | ----------------------------------------------------- |
| `WAYLOG_WRITE_KEY` | `/v1/events`, `/v1/otlp/v1/traces`, `/v1/signals` (SDKs, collectors, production signals) |
| `WAYLOG_READ_KEY`  | Read APIs, dashboard session                          |
| `WAYLOG_AGENT_KEY` | `/v1/tools/*`, `/v1/ask`, `/v1/plans/*`               |

`WAYLOG_API_KEY` is a legacy alias for the write scope. `ParseConfig` validates the auth matrix at startup and refuses to boot with an unsafe combination.

## Status

Public alpha. APIs may break before 1.0.

**Shipped:**

- Go SDK v2 (`net/http`, chi, gin, echo) and TypeScript SDK v2 (`@waylog/sdk`, ESM, Node 18+, standalone core, Express, Hono, Next.js, NestJS)
- OTLP/HTTP traces at `/v1/otlp/v1/traces` (Phase A — traces only)
- durable ingest with WAL + replay
- hot graph with flattened 3-node model + dedicated trace store
- schema-2.0 recent-index read APIs behind `WAYLOG_V2_READS=true`
- SQLite cold store (events, deployments, signals, incidents, causal claims)
- signal-driven incident engine with `waylog incidents`, `waylog incident <id>`, and dashboard incident cards
- 10 deterministic analysis tools, rollup-correct root-cause attribution
- agent-native REST (`/v1/tools/*`, `/v1/ask`, `/v1/plans/execute`) with idempotency and structured envelopes
- `/v1/traces/story` and indented failure-path rendering in the dashboard
- dashboard: minimal v2 triage loop (errors, explain, blast, recent requests)
- v2 operator CLI (`capabilities`, `recent`, `incidents`, `incident`, `errors`, `event`, `trace`, `explain`, `blast`, `search`) over read APIs
- live TUI (`waylog-live --dev` streams via SSE), MCP stdio
- scoped auth (write/read/agent) with startup validation

**Planned:**

- OTLP gRPC, logs, and metrics (Phase B)
- Python SDK
- Mintlify docs site

## Known limitations

- Single-node only. No HA, no clustering.
- Alpha quality. APIs may break before 1.0.
- OTLP is HTTP/traces only. gRPC, logs, and metrics are not shipping yet.
- Only Go and TypeScript SDKs today. Python / Java / Ruby are not available.
- SQLite cold store fits demos and small deployments; not sized for production-scale retention.
- Signal and incident records are SQLite-backed; they do not use the event WAL/replay path.
- Incident cause classification is deterministic and heuristic. `runtime` signals are accepted but do not produce a `runtime` cause label yet.
- No built-in alerting or paging. Waylog answers questions, it doesn't wake you up.
- No multi-tenancy. One instance = one trust boundary.
- No full log search, Slack/PagerDuty automation, RBAC/SSO, or automatic remediation.

**Fastest walkthrough:** `make demo`, open <http://localhost:9081/demo>, click **Run traffic burst**, then use the dashboard or `waylog incidents`, `waylog recent`, `waylog errors`, `waylog explain`, and `waylog blast` to answer what failed, which downstream was involved, and how broad the impact is.
