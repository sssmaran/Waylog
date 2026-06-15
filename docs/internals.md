# Internals

Mechanics behind the ingest server. If you're adopting Waylog for a service and want to know what's in the box, this is the right level of detail. For quick start, see the [README](../README.md). For env vars, see [env.md](env.md).

## Data flow

1. **SDK / collector** emits a schema-2.0 WideEvent over HTTP, or sends OTLP/HTTP traces that are converted to schema-2.0 events.
2. **Ingest** validates the event, writes it to the schema-2.0 WAL, and — only if the WAL write succeeds — projects it into the v2 reader's recent index and forwards it to the cold-store batch writer.
3. **v2 reader** (`internal/ingest/v2`) indexes events by event, trace, service, error family, and downstream call for `errors`, `recent`, `explain`, `blast`, and `events/search` endpoints.
4. **Cold store** (SQLite, optional) persists events, deployments, signals, and incident rows for historical queries and incident-engine rebuild.
5. **Incident engine** (`internal/incidents`) runs every `WAYLOG_INCIDENT_TICK_INTERVAL` against the v2 reader + signal store + deployment store, opens/updates/recovers/resolves incidents, and attaches propagation + blast evidence snapshots.
6. **Read path** serves the dashboard, CLI, MCP, and agent APIs from the v2 reader and the incident store.

## Durability model

The event log is the **source of truth**. The v2 reader's in-memory index is a derived, queryable view that can be rebuilt from the log.

### Write path

Every event must be durably logged before it enters the recent index. If the event log write fails, the handler returns 503 and the event is not projected. The client is expected to retry.

### Durability modes

- **Sync (default, `EVENT_LOG_SYNC=true`)** — each write `fsync`s to disk. Survives process crash, host crash, and power loss. ~200–1000 events/sec depending on the disk.
- **Buffered (`EVENT_LOG_SYNC=false`)** — writes go to the OS page cache without per-write fsync. Survives process crash only. Higher throughput, suitable for dev or load testing.

### Startup replay

On boot, the server replays event-log entries newer than `time.Now() - GRAPH_HOT_WINDOW` (default 24h) into the v2 reader. If replay fails, the server still becomes ready in **degraded mode**. New events ingest correctly; historical reads may return partial results until traffic rebuilds the index. `/healthz` reports `replay.status: "failed"` so operators can see it.

### Readiness policy

`/readyz` gates on ingest availability, not replay completeness. Fail-open: the server becomes ready as soon as it can accept events. Inspect `/healthz` for degraded state.

## Hot-window retention

The v2 reader's in-memory index is pruned every tick to enforce `GRAPH_HOT_WINDOW` (default 24h). Entries older than the window are dropped from the recent index. Cold storage (SQLite) retains the full history bounded by `EVENT_LOG_RETENTION`.

Retention bounds memory growth. Production deployments should tune this to match their incident-response window — you rarely need more than 24 hours of hot data in memory.

For the single-node throughput, memory, and storage ceiling as a whole — and how to tune within it or scale past it — see [`scale-and-limits.md`](scale-and-limits.md).

## Spike detection baseline

The incident engine opens an incident when an error family's current-window
count clears `WAYLOG_INCIDENT_MIN_COUNT` and its lift over baseline clears
`WAYLOG_INCIDENT_MIN_LIFT`. Two design choices keep the detector deterministic
and explainable (no learned models):

- **Baseline = per-family median of the 3 prior windows** (`[now-2W, now-W]`,
  `[now-3W, now-2W]`, `[now-4W, now-3W]`, where `W` is
  `WAYLOG_INCIDENT_WINDOW`). A family absent from a window counts as 0. The
  median means one anomalous prior window can neither suppress a real spike
  (a prior burst inflating the baseline) nor fabricate lift (a single quiet
  window deflating it). A family that is new or mostly-quiet has median 0 and
  is treated as a fresh spike (lift = current count). All four windows are
  served by the v2 reader and must fit inside `GRAPH_HOT_WINDOW` — the
  startup rebuild replay window is sized to `4 × WAYLOG_INCIDENT_WINDOW` for
  the same reason.
- **Low-traffic guard** (`WAYLOG_INCIDENT_MIN_RATE`, errors/minute, default
  `0` = disabled). On low-traffic services a handful of failures can clear
  `MIN_COUNT` while representing trivially small absolute volume; when set,
  a family must also sustain `MIN_RATE × window-minutes` failures in the
  current window to open an incident.

## Service attribution

The v2 reader carries per-request service info inferred from span fan-out:

- **`root_service`** (canonical owner) — the originating service for the trace, used for ownership metrics. One canonical service per request, no fan-out inflation.
- **`services`** (set) — every service that touched the request, used by topology-aware tools (`blast_radius`) where fan-out semantics are correct.

## Sampling

Sampling is hash-based on `trace_id` (FNV), so a given trace is either fully sampled or fully dropped — you never get half a propagation chain.

- Errors and slow requests bypass sampling.
- Happy-path sampling is controlled by `HAPPY_SAMPLE_RATE_PCT`. Dev profile uses `100`, prod profile uses `5`.
- Pre-sampling counters increment **after** WAL success so the event log remains the source of truth.

## Counter buffer

A 120-minute ring buffer keeps per-minute counts for fast windowed error-rate queries. For windows larger than 120 minutes, `Sum()` returns 0 and callers fall back to the v2 reader's index. This bounds memory while keeping short-window reads O(1).

## Event log rotation

Size-based rotation on `EVENT_LOG_MAX_FILE_MB`. When two rotations happen in the same second, `openNewFile()` adds a `-N` sequence suffix to avoid filename collisions. Retention (`EVENT_LOG_RETENTION`, default 72h) runs every 5 minutes and deletes files older than the window.

## Metrics

Custom `prometheus.Registry` per server — no global. All metric calls are guarded by `if s.metrics != nil` so tests can run without wiring a registry. Scraped at `/metrics` under the `waylog_*` prefix.

## SDK contract

See [`waylog-sdk-contract.md`](waylog-sdk-contract.md) for the schema-2.0 SDK contract. Key points:

- `schema_version = "2.0"`
- Trace ID: 32 hex chars. Span ID: 16 hex chars (W3C traceparent)
- Failed events should include `anchor.step`, `anchor.error_code`, and a matching failed step
- Suppressed events remain queryable only through explicit recent/search/direct trace surfaces and are excluded from errors/blast
