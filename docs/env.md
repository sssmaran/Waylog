# Environment Variables

Reference for configuring the Waylog ingest server and SDK. All variables are read at process start — change them and restart.

## Required for specific features

| Variable | Purpose |
|---|---|
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` | Required for CLI LLM-backed Ask flows |

## Auth

Scoped keys. See the Auth section of the [README](../README.md).

| Variable | Scope |
|---|---|
| `WAYLOG_WRITE_KEY` | Write auth for `/v1/events` (preferred) |
| `WAYLOG_API_KEY`   | Legacy alias for write scope. Supports `Authorization: Bearer` and `X-API-Key` headers |
| `WAYLOG_READ_KEY`  | Read auth for read endpoints + dashboard session validation |
| `WAYLOG_AGENT_KEY` | Agent auth for `/v1/tools/*`, `/v1/ask`, `/v1/plans/*`. No session fallback |
| `DASHBOARD_AUTH`   | Dashboard auth mode: `off` \| `basic:<user>:<pass>` \| `key:<secret>` |
| `DASHBOARD_SESSION_SECRET` | Session signing key (derived from `DASHBOARD_AUTH` if unset) |

`ParseConfig` validates the auth matrix at startup and refuses to boot with an unsafe combination.

## Ingest server

| Variable | Default | Purpose |
|---|---|---|
| `INGEST_ADDR` | `:8080` | Listen address |
| `MAX_BODY_BYTES` | `1048576` (1 MB) | Max body size for `/v1/events` |
| `READ_HEADER_TIMEOUT` | `5s` | HTTP read header timeout |
| `READ_TIMEOUT` | `10s` | HTTP read timeout |
| `WRITE_TIMEOUT` | `10s` | HTTP write timeout |
| `IDLE_TIMEOUT` | `120s` | HTTP idle timeout |
| `CORS_ORIGIN` | `*` | Allowed CORS origin for read APIs |

## Storage and persistence

| Variable | Default | Purpose |
|---|---|---|
| `SNAPSHOT_PATH` | `./data/graph_snapshot.json` | Graph snapshot location |
| `SQLITE_PATH` | — | SQLite cold store path (optional; disabled if empty) |
| `EVENT_LOG_DIR` | — | Append-only event log directory (disabled if empty) |
| `EVENT_LOG_V2_DIR` | `${EVENT_LOG_DIR}/v2` or `./data/eventlog-v2` | Raw schema-2.0 WAL directory for `/v1/events` |
| `EVENT_LOG_SYNC` | `true` | Per-write fsync. Set `false` for dev/load testing |
| `EVENT_LOG_MAX_FILE_MB` | `50` | Rotation size. `0` disables rotation |
| `EVENT_LOG_RETENTION` | `72h` | Event log retention. Must be positive |
| `WAYLOG_V2_DEDUP_CAPACITY` | `65536` | Recent schema-2.0 `event_id` dedupe cache capacity |
| `GRAPH_HOT_WINDOW` | `GRAPH_RETENTION` or `24h` | Recent in-memory graph/index retention window and max v2 read window |
| `GRAPH_RETENTION` | `24h` | Hot graph retention. Nodes older than this are pruned every snapshot tick |

See [Internals](internals.md) for the full durability model.

## Kafka transport

| Variable | Default | Purpose |
|---|---|---|
| `KAFKA_BROKERS` | `localhost:9092` | Kafka broker addresses |
| `KAFKA_TOPIC` | `wide_events` | Topic name for WideEvent ingestion |

## Feature flags

| Variable | Default | Purpose |
|---|---|---|
| `GRAPH_UI` | `false` | Enable Graph topology tab in dashboard and `/v1/topology` endpoint |
| `WAYLOG_V2_READS` | `false` | Route v2 read endpoints to the schema-2.0 recent index |
| `CAUSAL_ENABLED` | `false` | Enable shadow-mode causal inference |
| `CAUSAL_INTERVAL` | `30s` | Causal inference ticker interval |
| `HAPPY_SAMPLE_RATE_PCT` | `2` | Success-event sampling rate. Set `100` in dev profiles |
| `MCP_STDIO` | — | Set to `1` to run MCP stdio server instead of REPL |

## Dashboard links

Optional external links rendered in the dashboard header. Hidden if empty.

| Variable | Purpose |
|---|---|
| `PROMETHEUS_URL` | Link to Prometheus UI |
| `GRAFANA_URL`    | Link to Grafana UI |

## Profiles

Pre-baked `.env` files live in [`deploy/`](../deploy):

- `deploy/dev.env` — 100% sampling, graph UI on, causal on, verbose logging
- `deploy/prod.env` — 5% happy-path sampling, graph UI off by default, tighter retention

Use with `make docker-dev` or `make docker-prod`.
