<div align="center"><pre><img width="461" height="129" alt="Screenshot 2026-03-17 at 2 36 01 PM" src="https://github.com/user-attachments/assets/fdec0a1b-055a-47e3-b52c-cf1cf80fe373" />
</pre></div>

<p align="center">
  <strong>Structured logging that explains failure propagation.</strong><br>
  Drop-in SDKs (Go, TypeScript) or OTLP/HTTP. Agent-native by design.
</p>

<p align="center">
  <code>polyglot SDKs</code> · <code>agent-native API</code> · <code>failure tree</code> · <code>rollup-correct root cause</code>
</p>

<p align="center">
  <em>Public alpha — an impact-analysis engine for backend systems built on WideEvents.</em>
</p>

---

## What Waylog does

A request hits your API gateway, fans out to three services, and one of them fails. The gateway returns 502. Your logs say "upstream error." Waylog tells you exactly what happened:

```text
  trace 7f3a2b9c…   flow=purchase   user=standard   region=us-east-1

  api-gateway        502   GW_DOWNSTREAM         14 ms   (root)
      └─ checkout    502   CHK_DOWNSTREAM         9 ms
          └─ db      200   —                      3 ms
          └─ payment 502   PMT_502                5 ms   ← first failure

  blast radius:  12 requests · 8 users · 4 services
```

This is not log search. Waylog builds a live in-memory graph from every request flowing through your services. When you ask a question — "why did this trace fail?", "who is affected by `PMT_502`?", "what changed in the last 10 minutes?" — it walks the graph and returns a precomputed, structured answer. Root-cause rollups count the originating failure once, not once per propagated hop.

Run `make docker-dev` and see it yourself.

## How it works

1. **Capture** — services emit [WideEvents](docs/waylog-sdk-contract.md) via the Go or TypeScript SDK, or push OpenTelemetry spans to `/v1/otlp/v1/traces`. Every event is durably logged (WAL + fsync) before it enters the graph.
2. **Analyze** — the ingest server flattens spans into a hot in-memory graph (requests · services · errors · users · deployments). A dedicated trace store keeps span-level detail for drill-down. Deterministic tools walk the graph to answer specific questions: propagation chain, blast radius, what-changed, deploy correlation.
3. **Operator** — CLI, REST, MCP, TUI, and the embedded dashboard all query the same graph through the same tool registry. Every answer is also callable by agents as a structured tool with idempotency keys.

## Get traces in

All three paths feed the same hot graph. Pick whichever matches your stack.

### TypeScript SDK

```bash
npm install @waylog/sdk
```

```ts
import { waylog, useLogger } from "@waylog/sdk/express";

app.use(waylog({
  service: "checkout",
  env: "prod",
  ingestUrl: "http://localhost:8080",
  apiKey: process.env.WAYLOG_WRITE_KEY,
}));

app.post("/buy", (req, res) => {
  useLogger(req).info("cart loaded", { user_id: req.user.id, tier: "vip" });
  res.sendStatus(200);
});
```

`@waylog/sdk` is ESM-only, Node 18+, and ships standalone core APIs plus Express, Hono, Next.js, and NestJS entrypoints (`@waylog/sdk/express`, `@waylog/sdk/hono`, `@waylog/sdk/next`, `@waylog/sdk/nest`).

### Go SDK

```go
import (
    waylog "github.com/sssmaran/WaylogCLI/pkg"
    wayloghttp "github.com/sssmaran/WaylogCLI/pkg/http"
)

func main() {
    _ = waylog.Init(waylog.Config{
        Service:   "checkout",
        Env:       "prod",
        Version:   "1.2.3",
        IngestURL: "http://localhost:8080",
    })
    defer waylog.Shutdown(context.Background())

    http.Handle("/", wayloghttp.Middleware(yourHandler))
}
```

Schema 1.1 helpers (`WithErrorReason`, `WithErrorPath`, `WithParentRequestID`, `WithMetadataKey`, `WithAttempt`, `WithRetry`) are additive.

### OTLP/HTTP traces

Point your existing OpenTelemetry collector at `http://localhost:8080/v1/otlp/v1/traces`. Protobuf bodies are accepted (gzip optional) and spans convert to WideEvents on the way in. **Phase A covers traces over HTTP.** gRPC, logs, and metrics are not yet shipping.

## Quick start

```bash
make docker-dev
```

This starts the ingest server, embedded dashboard, Prometheus, Grafana, and four real Go services wired together with the Waylog SDK middleware (`api-gateway → checkout → db → payment`). No mocks.

Once the stack is up:

1. Open the demo app at <http://localhost:9081/demo> and click a button:
   - **Purchase (Success)** — healthy 4-service flow
   - **Purchase (DB Fail)** — `DB_503` cascading up through checkout and the gateway
   - **Purchase (Payment Fail)** — `PMT_502` cascading up through checkout
   - **Purchase (Checkout Fail)** — `CHK_500` short-circuit at checkout
2. Open the dashboard at <http://localhost:8080/ui> — KPIs, failing-traces banner, recent traces, and deploy-diff panel populate live via SSE.
3. Click into a failing trace to see both the flat propagation chain and the rendered tree.

Stop with `make docker-down`. Wipe persistent volumes with `make docker-reset`.

> `./scripts/demo-cascade-failure.sh` injects an equivalent fixture by POSTing synthetic events directly. It is a fixture, not a substitute for the live path above.

### Alternative: local ingest server (no Docker)

```bash
make ingest
```

Runs only the ingest server. Point your own services at it via an SDK or OTLP. Full env-var reference: [`docs/env.md`](docs/env.md).

## What you can ask

### CLI

```bash
waylog "show top errors"
waylog "explain request 7f3a2b..."
waylog "trace summary for 7f3a2b..."
waylog "graph_query expr='error_code=PMT_502' window='10m'"
waylog "compare_windows current='10m' baseline='10m' offset='1h'"
```

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

### Trace story as a tree

```bash
curl "http://localhost:8080/v1/traces/story?trace_id=$TRACE&format=tree" \
  -H "Authorization: Bearer $WAYLOG_READ_KEY"
```

Returns the nested propagation tree the dashboard renders. Omit `format=tree` for the flat chain.

### MCP (agent surface)

```bash
make ingest-mcp    # MCP_STDIO=1
```

Exposes the same tool registry over MCP stdio for Claude, Cursor, and other MCP clients. Same semantics as the REST API.

### Analysis tools

All ten tools are deterministic, idempotent, and available via CLI, REST `/v1/tools/{name}`, MCP, and plan execution.

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

Full schemas: `GET /v1/tools` or [`docs/openapi.yaml`](docs/openapi.yaml).

## Dashboard

The embedded dashboard at `/ui` is the fastest way to see a running system:

- Geist dark theme with a light-mode toggle
- failing-traces banner at the top of the bento layout
- KPI overview with time series (error rate, p50/p95/p99)
- recent traces with flat-chain **and** tree views in the trace modal
- **most likely failure origin** — root-cause attribution per window (rollup-correct)
- **who's affected** — user cohort by tier and region
- **what changed** — deploy-diff panel with causal shadow-mode claims
- graph topology (Cytoscape, cose layout) — gated by `GRAPH_UI=1`
- SSE live updates, no polling

<!-- TODO: dashboard screenshot -->

## Architecture

```text
Go / TS services (SDK) · OTLP/HTTP collectors
        │  WideEvents (HTTP or Kafka) · OTLP traces
        ▼
  ingest server
    ├─ event log (append-only WAL, source of truth)
    ├─ hot graph  (requests · services · errors · users · deploys)
    ├─ trace store (span-level detail, time-bucketed)
    ├─ SQLite cold store (events · deployments · causal claims)
    ├─ tool registry · Ask · plan execution
    └─ SSE dashboard · health · metrics · OpenAPI
        │
        ├──▶ /ui dashboard (Geist theme, Chart.js, Cytoscape.js)
        ├──▶ /v1/tools/* · /v1/plans/execute (agent-native)
        └──▶ CLI · TUI · MCP · agents
```

The hot graph is a flattened 3-node-type model (request, service, error); span detail lives in a dedicated trace store. Events are durably logged before entering the graph — if the process crashes, replay rebuilds the graph from the WAL on next boot.

Durability model, retention, merge semantics, readiness policy, and counter buffer: [`docs/internals.md`](docs/internals.md). Full HTTP contract: [`docs/openapi.yaml`](docs/openapi.yaml).

## Development

```bash
make build          # core binaries
make build-examples # demo services
make fmt vet test   # checks
make test-race      # race detector
make ts-test        # TypeScript SDK vitest suite
make ci             # fmt + vet + test-race + test-sdk + ts-test + doc-link + rollup-contract
```

## Auth

Waylog uses three scoped keys. They are independent — the dashboard never holds the agent key.

| Key                | Protects                                |
| ------------------ | --------------------------------------- |
| `WAYLOG_WRITE_KEY` | `/v1/events`, `/v1/otlp/v1/traces` (SDKs, collectors) |
| `WAYLOG_READ_KEY`  | Read APIs, dashboard session            |
| `WAYLOG_AGENT_KEY` | `/v1/tools/*`, `/v1/ask`, `/v1/plans/*` |

`WAYLOG_API_KEY` is a legacy alias for the write scope. `ParseConfig` validates the auth matrix at startup and refuses to boot with an unsafe combination.

## Status

Public alpha. APIs may break before 1.0.

**Shipped:**

- Go SDK (schema 1.1 accessors) and TypeScript SDK (`@waylog/sdk`, ESM, Node 18+, Express + Hono middleware)
- OTLP/HTTP traces at `/v1/otlp/v1/traces` (Phase A — traces only)
- durable ingest with WAL + replay
- hot graph with flattened 3-node model + dedicated trace store
- SQLite cold store (events, deployments, causal claims)
- 10 deterministic analysis tools, rollup-correct root-cause attribution
- agent-native REST (`/v1/tools/*`, `/v1/ask`, `/v1/plans/execute`) with idempotency and structured envelopes
- `/v1/traces/story?format=tree` and tree rendering in the dashboard
- dashboard: Geist theme + light-mode toggle, failing-traces banner, bento layout, deploy-diff, SSE live
- live TUI (`waylog-live --dev` streams via SSE), MCP stdio, CLI with LLM tool routing
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
- No built-in alerting or paging. Waylog answers questions, it doesn't wake you up.
- No multi-tenancy. One instance = one trust boundary.

**Fastest walkthrough:** `make docker-dev`, open <http://localhost:9081/demo>, click a failure button, then open <http://localhost:8080/ui> to see the propagation chain live.
