<div align="center"><pre><img width="461" height="129" alt="Screenshot 2026-03-17 at 2 36 01 PM" src="https://github.com/user-attachments/assets/fdec0a1b-055a-47e3-b52c-cf1cf80fe373" />
</pre></div>

<p align="center">
  <strong>See how failures propagate through your services.</strong><br>
  Impact analysis for backend systems. Agent-native by design.
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

This is not a log search. Waylog builds a live in-memory graph from every request flowing through your services. When you ask a question — "why did this trace fail?", "who is affected by PMT_502?", "what changed in the last 10 minutes?" — it walks the graph and returns a precomputed, structured answer.

Every line above is real output from the live demo. Run `make docker-dev`, open `http://localhost:9081/demo`, click **Purchase (Payment Fail)**, and see it yourself.

**Agent-native by design.** Every answer is available as a deterministic HTTP tool call with structured outputs and idempotency keys. Agents and humans hit the same API — no scraping a chat UI, no brittle log regexes.

## How it works

1. **Ingest** — services emit [WideEvents](docs/waylog-sdk-contract.md) over HTTP via the Go SDK, or push spans to the OTLP/HTTP endpoint at `/v1/otlp/v1/traces`. Every event is durably logged (WAL + fsync) before it enters the graph.
2. **Build** — the ingest server flattens spans into a hot in-memory graph of requests, services, errors, users, and deployments. No external database in the hot path.
3. **Traverse** — 10 deterministic tools walk the graph to answer specific questions: propagation chain, blast radius, failure patterns, what changed.
4. **Query** — CLI, REST, MCP, TUI, dashboard, and agent plan execution all query the same graph through the same tool registry.

## Get traces in

Waylog supports two ingestion paths. Both feed into the same hot graph — the dashboard, tools, and APIs light up identically regardless of which path you use.

### OTLP/HTTP traces (Phase A)

Point your existing OpenTelemetry collector at `http://localhost:8080/v1/otlp/v1/traces`. The endpoint accepts protobuf (with optional gzip), converts spans to WideEvents, and merges them into the graph. Phase A covers traces over HTTP; gRPC, logs, and metrics are not yet shipping.

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

The SDK validates `Service`, `Env`, and exactly one transport (`IngestURL`, Kafka brokers, or a custom transport) at init time.

## Quick start

```bash
make docker-dev
```

This starts the full stack: the ingest server, embedded dashboard, Prometheus, Grafana, and four real Go services wired together with the Waylog SDK middleware (`api-gateway → checkout → db → payment`). No mocks, no synthetic data — these are real services making real HTTP calls.

Once the stack is up:

1. Open the demo app at <http://localhost:9081/demo> and click a button:
   - **Purchase (Success)** — healthy 4-service flow
   - **Purchase (DB Fail)** — `DB_503` cascading up through checkout and the gateway
   - **Purchase (Payment Fail)** — `PMT_502` cascading up through checkout
   - **Purchase (Checkout Fail)** — `CHK_500` short-circuit at checkout
2. Open the dashboard at <http://localhost:8080/ui> — KPIs, recent traces, and topology populate live via SSE.
3. Click into a failing trace to see the rendered propagation chain.

Stop with `make docker-down`. Wipe persistent volumes with `make docker-reset`.

> `./scripts/demo-cascade-failure.sh` injects an equivalent fixture by POSTing synthetic events directly. It's a fixture, not a substitute for the live path above.

### Alternative: local ingest server (no Docker)

```bash
make ingest
```

Runs only the ingest server. Useful when developing the server itself; you'll need to point your own service at it via the SDK or OTLP. See `docs/env.md` for the full environment variable reference.

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

### MCP (agent surface)

```bash
make ingest-mcp    # MCP_STDIO=1
```

Exposes the same tool registry over MCP stdio for Claude, Cursor, and other MCP clients. Same semantics as the REST API.

## Dashboard

The embedded dashboard at `/ui` is the fastest way to see a running system:

- KPI overview with time series (error rate, p50/p95/p99)
- recent traces and trace drill-down with the propagation chain view
- **most likely failure origin** — root cause attribution per window
- **who's affected** — user cohort by tier and region
- **what changed** — deploy correlation with causal shadow-mode claims
- graph topology (Cytoscape, cose layout) — gated by `GRAPH_UI=1`
- SSE live updates, no polling

<!-- TODO: dashboard screenshot -->

## Analysis tools

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

Full schemas: `GET /v1/tools` or `docs/openapi.yaml`.

## Architecture

```text
Go services (SDK) · OTLP/HTTP collectors
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
        ├──▶ /ui dashboard (Geist dark theme, Chart.js, Cytoscape.js)
        ├──▶ /v1/tools/* · /v1/plans/execute (agent-native)
        └──▶ CLI · TUI · MCP · agents
```

The hot graph is a flattened 3-node-type model (request, service, error) with span detail offloaded to a dedicated trace store. This keeps the graph lean for topology queries while preserving full span trees for drill-down. Events are durably logged before entering the graph — if the process crashes, replay rebuilds the graph from the WAL on next boot.

Internals (durability model, retention, graph merge semantics, readiness policy, counter buffer): see [`docs/internals.md`](docs/internals.md).

## HTTP API

| Method   | Path                                                      | Scope       |
| -------- | --------------------------------------------------------- | ----------- |
| POST     | `/v1/events`                                              | write       |
| POST     | `/v1/events/validate`                                     | write       |
| POST     | `/v1/otlp/v1/traces`                                      | write       |
| GET      | `/v1/events/search`                                       | read        |
| GET      | `/v1/traces/recent` · `/v1/traces/story`                  | read        |
| GET      | `/v1/overview` · `/v1/overview/timeseries` · `/v1/routes` | read        |
| GET/POST | `/v1/deployments`                                         | read/write  |
| GET      | `/v1/topology` · `/v1/blast_radius`                       | read        |
| GET      | `/v1/stream/dashboard`                                    | read (SSE)  |
| GET      | `/v1/tools` · POST `/v1/tools/{name}`                     | agent       |
| POST     | `/v1/ask` · `/v1/plans/execute`                           | agent       |
| GET      | `/v1/stream/plans/{id}`                                   | agent (SSE) |
| GET      | `/livez` · `/readyz` · `/healthz` · `/metrics`            | —           |

Full contract: [`docs/openapi.yaml`](docs/openapi.yaml).

## Documentation

| Doc                                                          | What it covers                                                         |
| ------------------------------------------------------------ | ---------------------------------------------------------------------- |
| [`docs/internals.md`](docs/internals.md)                     | Durability model, retention, merge semantics, sampling, counter buffer |
| [`docs/env.md`](docs/env.md)                                 | Environment variable reference                                         |
| [`docs/openapi.yaml`](docs/openapi.yaml)                     | Full HTTP API contract                                                 |
| [`docs/waylog-sdk-contract.md`](docs/waylog-sdk-contract.md) | Language-agnostic WideEvent schema for SDK authors                     |

## Development

```bash
make build          # core binaries
make build-examples # demo services
make fmt vet test   # checks
make test-race      # race detector
make ci             # fmt + vet + test-race
```

## Auth

Waylog uses three scoped keys. They are independent — the dashboard never holds the agent key.

| Key                | Protects                                |
| ------------------ | --------------------------------------- |
| `WAYLOG_WRITE_KEY` | `/v1/events` (SDKs, collectors)         |
| `WAYLOG_READ_KEY`  | Read APIs, dashboard session            |
| `WAYLOG_AGENT_KEY` | `/v1/tools/*`, `/v1/ask`, `/v1/plans/*` |

`WAYLOG_API_KEY` is kept as a legacy alias for the write scope. The dashboard uses server-side `/ui/ask` and `/ui/explain` handlers so browsers never see the agent key. `ParseConfig` validates the auth matrix at startup and refuses to boot with an unsafe combination.

## Known limitations

- Single-node only. No HA, no clustering.
- Alpha quality. APIs may break before 1.0.
- Go SDK is the only stable transport. OTLP/HTTP traces are Phase A — functional end-to-end but not yet battle-tested at scale.
- SQLite cold store fits demos and small deployments; not sized for production-scale retention.
- No built-in alerting or paging. Waylog answers questions, it doesn't wake you up.
- No multi-tenancy. One instance = one trust boundary.

## Support matrix

| Capability        | Today                 | Next                          |
| ----------------- | --------------------- | ----------------------------- |
| Ingest            | Go SDK + OTLP/HTTP    | OTLP gRPC (Phase B)          |
| Languages         | Go                    | Python, TypeScript via OTLP   |
| Deploy mode       | Single-node, Docker   | —                             |
| Cold storage      | SQLite                | Postgres (Phase 2)            |
| HA / multi-node   | Not supported         | Not on alpha roadmap          |

## Status

Public alpha. APIs may break before 1.0.

- Go SDK v2 with HTTP transport
- OTLP/HTTP traces ingestion at `/v1/otlp/v1/traces` (Phase A — traces only, end-to-end into the hot graph)
- durable ingest with WAL + replay
- hot graph with flattened 3-node model + dedicated trace store
- SQLite cold store (events, deployments, causal claims)
- 10 deterministic analysis tools
- deploy tracking + causal shadow mode
- agent-native REST (`/v1/tools/*`, `/v1/ask`, `/v1/plans/execute`) with idempotency and structured envelopes
- embedded dashboard with SSE, topology (Cytoscape), and trace drill-down
- live TUI, MCP stdio, CLI with LLM tool routing
- scoped auth (write/read/agent) with startup validation

**Planned:**

- OTLP gRPC, logs, and metrics (Phase B)
- Python and TypeScript SDKs
- broader language coverage via OTLP

**Fastest walkthrough:** `make docker-dev`, open <http://localhost:9081/demo>, click a failure button, then open <http://localhost:8080/ui> to see the propagation chain live.
