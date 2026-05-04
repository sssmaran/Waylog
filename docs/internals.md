# Internals

Mechanics behind the ingest server. If you're adopting Waylog for a service and want to know what's in the box, this is the right level of detail. For quick start, see the [README](../README.md). For env vars, see [env.md](env.md).

## Data flow

1. **SDK / collector** emits a schema-2.0 WideEvent over HTTP, or sends OTLP/HTTP traces that are converted to schema-2.0 events.
2. **Ingest** validates the event, writes it to the schema-2.0 WAL, and — only if the WAL write succeeds — projects it into recent read models.
3. **Derived read models** index events by event, trace, service, error family, and downstream call for `recent`, `errors`, `explain`, and `blast`.
4. **Cold store** (SQLite, optional) persists events, deployments, and causal claims for historical queries.
5. **Snapshot** writes the graph to disk every tick (default 5s) so restarts replay only the tail.
6. **Read path** serves the dashboard, CLI, MCP, and agent APIs from the same derived data.

## Durability model

The event log is the **source of truth**. The in-memory graph and schema-2.0 read indexes are derived, queryable views that can be rebuilt from the log.

### Write path

Every event must be durably logged before it enters the graph. If the event log write fails, the handler returns 503 and the event is not merged. The client is expected to retry.

### Durability modes

- **Sync (default, `EVENT_LOG_SYNC=true`)** — each write `fsync`s to disk. Survives process crash, host crash, and power loss. ~200–1000 events/sec depending on the disk.
- **Buffered (`EVENT_LOG_SYNC=false`)** — writes go to the OS page cache without per-write fsync. Survives process crash only. Higher throughput, suitable for dev or load testing.

### Snapshot persistence

The graph is snapshotted to disk every 5s by default. The persist layer uses **atomic write**: write a temp file, `fsync`, move the previous good snapshot to `.bak`, then rename the temp file into place. A crash during save never corrupts the current snapshot; the `.bak` is the previous good state.

### Startup replay

On boot, the server loads the latest snapshot and then replays event log entries newer than the snapshot to rebuild the graph. If replay fails, the server starts with whatever data the snapshot had and becomes ready in **degraded mode**. New events ingest correctly; historical reads (story, overview, recent) may return partial results until traffic rebuilds the graph. `/healthz` reports `replay.status: "failed"` so operators can see it.

### Readiness policy

`/readyz` gates on ingest availability, not replay completeness. Fail-open: the server becomes ready as soon as it can accept events. Inspect `/healthz` for degraded state.

## Graph retention

The graph is pruned every snapshot tick to enforce `GRAPH_RETENTION` (default 24h). Nodes whose `LastSeen` is older than the retention window are dropped along with their edges. `PruneOlderThan` rebuilds all derived indexes (edge set, trace maps, request facts, counters) and then snapshots the pruned graph.

Retention bounds memory growth. Production deployments should tune this to match their incident-response window — you rarely need more than 24 hours of hot graph data.

## Graph merge semantics

Events arrive out-of-order and across services. The merge rules for a request node:

- **`success`** is AND-reduced across hops. A request is successful only if every span is.
- **Root span** overwrites the request summary (flow, user, latency) and sets `root_service`.
- **`error_codes`** accumulate and are deduped.
- **`RequestFacts`** (topology view) is always updated on merge; counter recompute is gated by `factsEqual` on `Services`, `Errors`, and `Flags`.

Edge rules:

- Edges are created only when both endpoints exist. Never create an edge pointing at a non-existent node.
- Error nodes are created only when `ev.Error != nil`. No empty error nodes from successful events.

## Service attribution

Request nodes carry two kinds of service information:

- **`root_service`** (canonical owner) — set by `mergeRequestAttrs` when the root span merges. Used by `/v1/routes` for ownership metrics. One canonical service per request, no fan-out inflation.
- **`Services []string`** in `RequestFacts` — populated from `handled_by` edges (every service that touched the request). Used by topology analysis tools (`blast_radius`, `failure_patterns`, etc.) where fan-out semantics are correct.

If the root span hasn't arrived yet, `/v1/routes` falls back to deriving service from the `event_name` prefix (`"api-gateway.request"` → `"api-gateway"`). Once the root merges, `root_service` takes precedence.

## Sampling

Sampling is hash-based on `trace_id` (FNV), so a given trace is either fully sampled or fully dropped — you never get half a propagation chain.

- Errors and slow requests bypass sampling.
- Happy-path sampling is controlled by `HAPPY_SAMPLE_RATE_PCT`. Dev profile uses `100`, prod profile uses `5`.
- Pre-sampling counters increment **after** WAL success so the event log remains the source of truth.

## Counter buffer

A 120-minute ring buffer keeps per-minute counts for fast windowed queries (`graph_insights`, `/v1/overview`). For windows larger than 120 minutes, `Sum()` returns 0 and callers fall back to the hot graph itself. This bounds memory while keeping short-window reads O(1).

## Event log rotation

Size-based rotation on `EVENT_LOG_MAX_FILE_MB`. When two rotations happen in the same second, `openNewFile()` adds a `-N` sequence suffix to avoid filename collisions. Retention (`EVENT_LOG_RETENTION`, default 72h) runs every 5 minutes and deletes files older than the window.

## Metrics

Custom `prometheus.Registry` per server — no global. All metric calls are guarded by `if s.metrics != nil` so tests can run without wiring a registry. 22+ collectors under the `waylog_*` prefix. Scraped at `/metrics`.

## SDK contract

See [`waylog-sdk-contract.md`](waylog-sdk-contract.md) for the schema-2.0 SDK contract. Key points:

- `schema_version = "2.0"`
- Trace ID: 32 hex chars. Span ID: 16 hex chars (W3C traceparent)
- Failed events should include `anchor.step`, `anchor.error_code`, and a matching failed step
- Suppressed events remain queryable only through explicit recent/search/direct trace surfaces and are excluded from errors/blast
