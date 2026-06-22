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

## Traffic anomaly detection

Error-spike detection is blind to the *silent outage*: request volume collapses
with zero errors (an upstream stops calling, a deploy wedges intake). When
`WAYLOG_TRAFFIC_ANOMALY_ENABLED=true`, a parallel detector keys on **per-service
request volume** (all statuses) using the **same median-of-3-prior-windows
baseline** as the error detector — deterministic, no learned model.

- **Drop**: open when `current ≤ WAYLOG_TRAFFIC_DROP_FACTOR × baseline`
  (default `0.5`). **Surge**: open when
  `current ≥ WAYLOG_TRAFFIC_SURGE_FACTOR × baseline` (default `3.0`; `0` disables).
- **Low-volume floor** (`WAYLOG_TRAFFIC_MIN_VOLUME`, default `20`): a service is
  eligible only if its baseline clears the floor — this excludes bursty
  low-traffic services and makes "drop from nothing" / divide-by-zero impossible.
- **Sustained gate** (`WAYLOG_TRAFFIC_SUSTAINED_TICKS`, default `2`): the anomaly
  must hold for N consecutive ticks before an incident opens, damping
  single-tick noise.
- A traffic incident is a normal incident keyed by a synthetic family
  (`step=<traffic>`, `error_code=TRAFFIC_DROP|TRAFFIC_SURGE`), so it reuses the
  whole pipeline — lifecycle, classifier (incl. **deploy correlation**, so a drop
  right after a deploy classifies `cause=deploy`), `suspect_change`, notify, and
  the deterministic `report_hash`/`evidence_fingerprint`.

**Known limitation (honest):** the baseline is the median of the *recent* prior
windows (~30 min), so this is **local relative** detection, not daily/weekly
seasonal. It catches sudden drops/surges but a planned, gradual maintenance
ramp-down can trip a drop alert. A seasonal/ML baseline is intentionally out of
scope — it would break determinism. Conservative defaults + the floor + the
sustained gate keep false positives low; the feature is off by default.

## Latency anomaly detection

The third RED leg (Duration). With `WAYLOG_LATENCY_ANOMALY_ENABLED=true`, a
detector tracks a configurable **tail percentile** of per-request `DurationMS`
per service (`WAYLOG_LATENCY_PERCENTILE`, default `95`), computed deterministically
by nearest-rank (sort + index), against the **same median-of-3-prior-windows**
baseline. A spike opens when `current ≥ WAYLOG_LATENCY_FACTOR × baseline`
(default `2.0`). Spike-only — a latency *drop* is not an incident.

Two floors keep it honest, addressing the classic latency-detection mistakes:

- **Sample floor** (`WAYLOG_LATENCY_MIN_REQUESTS`, default `50`): a percentile over
  a few requests is meaningless. A service is eligible only when the current window
  *and* ≥2 of the 3 baseline windows each clear the floor — so there is never a
  "0 baseline → infinite spike" false positive.
- **Absolute floor** (`WAYLOG_LATENCY_MIN_MS`, default `0` = off): stops a large
  *ratio* on tiny absolute latencies (3 ms → 9 ms) from alerting.

Plus the shared **sustained gate** (`WAYLOG_LATENCY_SUSTAINED_TICKS`, default `2`).
A latency incident is a synthetic-family incident (`step=<latency>`,
`error_code=LATENCY_SPIKE`) that reuses the whole pipeline — lifecycle, classifier
(incl. deploy correlation, so a spike right after a deploy classifies
`cause=deploy`), `suspect_change`, notify, and the deterministic
`report_hash`/`evidence_fingerprint`.

**Cost & limits (honest):** the percentile needs a per-service sort over the
window's durations each tick (the one scaling-sensitive piece — bounded by
`GRAPH_HOT_WINDOW`). Same local-window, non-seasonal limitation as volume.
Per-**service** percentiles can mask a single slow **route**; per-route latency is
a documented future refinement. Off by default.

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
