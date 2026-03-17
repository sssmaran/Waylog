<div align="center"><pre><img width="461" height="129" alt="Screenshot 2026-03-17 at 2 36 01 PM" src="https://github.com/user-attachments/assets/fdec0a1b-055a-47e3-b52c-cf1cf80fe373" />
</pre></div>

<p align="center">
  <strong>Agent-native observability for incident triage.</strong><br>
  Go SDK • Hot graph analysis • SQLite cold storage • SSE dashboard • Deterministic tool APIs • Plan execution
</p>

---

## What is WAYLOG?
Waylog is an agent-native observability platform for incident triage.
It ingests structured request events from Go services, builds a hot in-memory graph for real-time analysis, persists cold history in SQLite, and exposes deterministic APIs for failure investigation, deploy correlation, blast-radius analysis, and multi-step plan execution.

The current repo includes:

- a Go SDK for HTTP or Kafka-backed event ingestion
- an ingest server with read APIs, direct tool execution, Ask, and deterministic plans
- an embedded dashboard with SSE live updates
- a CLI, Bubble Tea live TUI, and MCP-compatible agent surface
- a containerized demo stack with Kafka, Prometheus, Grafana, and sample services

## Why WAYLOG?

Waylog is built for the common incident loop:

1. something is failing
2. which service is the most likely failure origin?
3. who is affected?
4. what changed recently?

Instead of treating observability as raw logs plus a chat box, Waylog models requests, services, errors, users, and deployments as graph entities and lets both humans and agents query that graph directly.

## Core Capabilities

- **Go SDK**: instrument services with `waylog.Init(...)` and `wayloghttp.Middleware(...)`
- **Hybrid storage**: hot in-memory graph plus SQLite cold store and append-only event log
- **Deterministic tools**: 11 graph analysis tools including `explain_request`, `failure_patterns`, `blast_radius`, `compare_windows`, and `graph_insights`
- **Agent-native API**: direct `/v1/tools/*`, `/v1/ask`, and `/v1/plans/execute` endpoints with structured errors, idempotency, and response envelopes
- **Deploy-aware incident triage**: deployment tracking, "What Changed", causal shadow-mode claims, and blast-radius APIs
- **Real-time UI**: embedded dashboard at `/ui` with SSE updates, failure-origin analysis, topology, trace drilldown, and deploy correlation
- **Demo and ops stack**: Docker Compose services, Prometheus, Grafana, demo scripts, and integration tests

## Architecture

```text
Go services / SDK / demo services
        |
        |  HTTP or Kafka wide events
        v
  ingest server
    |- hot graph store
    |- append-only event log
    |- SQLite cold store
    |- tool registry + Ask + plan execution
    |- SSE dashboard / plan progress streams
    `- health + metrics + OpenAPI
        |
        +--> /ui dashboard
        +--> /v1/tools/* and /v1/plans/execute
        +--> CLI / MCP / agents
```

## Prerequisites

- Go 1.24+
- Docker and Docker Compose for the full demo stack
- a Gemini or Google API key if you want to use Ask / LLM-backed flows

## Quick Start

### Option 1: full local stack with Docker

This is the fastest way to see the product end to end.

```bash
make docker-dev
```

Then open:

- dashboard: [http://localhost:8080/ui](http://localhost:8080/ui)
- Prometheus: [http://localhost:9090](http://localhost:9090)
- Grafana: [http://localhost:3000](http://localhost:3000)

The default dev profile enables:

- `HAPPY_SAMPLE_RATE_PCT=100`
- `GRAPH_UI=1`
- `CAUSAL_ENABLED=true`
- SQLite cold storage at `/data/waylog.db`

To stop the stack:

```bash
make docker-down
```

To wipe volumes:

```bash
make docker-reset
```

### Option 2: run the ingest server directly

```bash
make ingest
```

Useful environment variables:

- `INGEST_ADDR` default `:8080`
- `SNAPSHOT_PATH` default `./data/graph_snapshot.json`
- `EVENT_LOG_DIR` for append-only JSONL event storage
- `SQLITE_PATH` for cold storage
- `WAYLOG_API_KEY` for write auth on `/v1/events`
- `WAYLOG_AGENT_KEY` for agent endpoints such as `/v1/tools/*`, `/v1/ask`, and `/v1/plans/execute`
- `GEMINI_API_KEY` or `GOOGLE_API_KEY` for Ask / LLM-backed flows

### Option 3: run the demo services

```bash
make micro-demo
```

Or use the scripted scenarios:

```bash
./scripts/demo-cascade-failure.sh
./scripts/demo-deploy-failure.sh
./scripts/demo-comparison.sh
./scripts/demo-agent-triage.sh
```

## SDK Example

```go
package main

import (
	"context"
	"os"

	"github.com/sssmaran/WaylogCLI/pkg/waylog"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
)

func main() {
	if err := waylog.Init(waylog.Config{
		Service:      "checkout",
		Env:          "dev",
		Version:      "1.2.3",
		DeploymentID: os.Getenv("DEPLOY_ID"),
		IngestURL:    "http://localhost:8080",
	}); err != nil {
		panic(err)
	}
	defer waylog.Shutdown(context.Background())

	_ = wayloghttp.Middleware(nil)
}
```

The SDK validates required config at init time:

- `Service` is required
- `Env` is required
- exactly one transport source should be used: `IngestURL`, Kafka brokers, or a custom transport

## Main Interfaces

### Dashboard

The embedded dashboard is served by the ingest server:

- `GET /ui`
- `GET /v1/stream/dashboard`

The dashboard includes:

- KPI overview and time series
- recent traces and trace drilldown
- "Most likely failure origin"
- "Who's affected"
- deploy-aware "What Changed"
- topology and routes views

### HTTP API

Important endpoints:

- `POST /v1/events`
- `POST /v1/events/validate`
- `GET /v1/events/search`
- `GET /v1/traces/recent`
- `GET /v1/traces/story`
- `GET /v1/routes`
- `GET /v1/deployments`
- `POST /v1/deployments`
- `GET /v1/topology`
- `GET /v1/blast_radius`
- `GET /v1/tools`
- `POST /v1/tools/{name}`
- `POST /v1/ask`
- `POST /v1/plans/execute`
- `GET /v1/stream/plans/{id}`

OpenAPI lives at [docs/openapi.yaml](docs/openapi.yaml).

### CLI

The CLI currently supports:

```bash
go run ./cmd/waylog "show top errors"
go run ./cmd/waylog "explain request <trace-id>"
go run ./cmd/waylog tools
```

Today, the CLI ask path still runs against a local graph snapshot plus local LLM/tool execution, while tool discovery prefers the ingest server when available.

### Live TUI

```bash
make waylog-live
```

## Tool and Plan Examples

If `WAYLOG_AGENT_KEY` is configured, include `Authorization: Bearer <key>` or `X-API-Key: <key>` on agent-facing requests.

List tools:

```bash
curl http://localhost:8080/v1/tools
```

Call a deterministic tool:

```bash
curl -X POST http://localhost:8080/v1/tools/failure_patterns \
  -H 'Content-Type: application/json' \
  -d '{"window":"10m"}'
```

Execute a deterministic plan:

```bash
curl -X POST http://localhost:8080/v1/plans/execute \
  -H 'Content-Type: application/json' \
  -d '{
    "steps": [
      {
        "id": "patterns",
        "tool": "failure_patterns",
        "params": {"window": "10m"}
      }
    ]
  }'
```

## Development

Build binaries:

```bash
make build
make build-examples
```

Run checks:

```bash
make fmt
make vet
make test
make test-race
```

CI currently runs:

- `gofmt -l ./cmd ./internal ./pkg ./examples`
- `go vet ./...`
- `go test -race -timeout 120s ./...`

## Repository Map

- [cmd/ingest/main.go](cmd/ingest/main.go): ingest server and route wiring
- [cmd/waylog/main.go](cmd/waylog/main.go): CLI entry point
- [internal/ingest](internal/ingest): handlers, envelope, dedup, SSE, plans
- [internal/tools](internal/tools): deterministic graph tools and schemas
- [internal/graph](internal/graph): hot graph store, analysis, causal logic
- [internal/coldstore](internal/coldstore): SQLite-backed persistence
- [internal/dashboard/static/index.html](internal/dashboard/static/index.html): embedded dashboard
- [pkg/waylog](pkg/waylog): SDK
- [scripts](scripts): demos and local automation helpers
- [docs/openapi.yaml](docs/openapi.yaml): HTTP API contract

## Auth Model

Waylog uses scoped keys:

- `WAYLOG_API_KEY`: write access for `/v1/events`
- `WAYLOG_AGENT_KEY`: agent-facing execution endpoints

The dashboard does not hold the agent key directly. Browser-facing dashboard flows use server-side UI handlers such as `/ui/ask` and `/ui/explain`.

## Current Status

The repo has moved beyond a logging demo. It now includes:

- SDK v2 HTTP transport
- durable ingest and replay
- SQLite cold storage
- deploy tracking and causal shadow mode
- real-time incident triage dashboard
- deterministic plan execution with SSE progress streaming

If you want the fastest product walkthrough, start with `make docker-dev`, open `/ui`, and run one of the demo scripts.

The current product is strongest for Go services today. Python and TypeScript SDKs, the OTel adapter, and broader deployment work are part of the planned expansion, not shipped capabilities yet.
