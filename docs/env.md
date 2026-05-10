# Environment Variables

Reference for configuring the Waylog ingest server and SDK. All variables are read at process start — change them and restart.

## Required for specific features

| Variable | Purpose |
|---|---|
| Provider credentials | Required only when natural-language Ask should call a configured LLM provider. Set the matching key for the selected provider: `GEMINI_API_KEY` or `GOOGLE_API_KEY`, `ANTHROPIC_API_KEY`, or `OPENAI_API_KEY` |

## LLM provider

Deterministic tools, plans, triage, MCP, and read APIs do not require an LLM provider. The provider is only used by natural-language Ask flows. If Ask cannot construct the selected provider, it returns a provider-agnostic "LLM provider not configured" error.

| Variable | Default | Purpose |
|---|---|---|
| `WAYLOG_LLM_PROVIDER` | `none` unless a supported provider key is present | LLM provider for Ask. Supported values: `none`, `gemini`, `anthropic`, `openai` |
| `WAYLOG_LLM_MODEL` | provider default | Provider-neutral model override. Takes precedence over provider-specific model variables |
| `GEMINI_MODEL` | `gemini-2.5-flash` | Gemini-specific model override when `WAYLOG_LLM_MODEL` is unset |
| `GEMINI_API_BASE` | Gemini API default | Gemini-specific API base URL override |
| `GEMINI_TOOL_MODE` | `text` | Gemini-specific tool-calling mode |
| `ANTHROPIC_API_KEY` | — | Anthropic API key for `WAYLOG_LLM_PROVIDER=anthropic` |
| `ANTHROPIC_MODEL` | `claude-sonnet-4-6` | Anthropic-specific model override when `WAYLOG_LLM_MODEL` is unset |
| `ANTHROPIC_API_BASE` | Anthropic API default | Anthropic-specific API base URL override |
| `OPENAI_API_KEY` | — | OpenAI API key for `WAYLOG_LLM_PROVIDER=openai` |
| `OPENAI_MODEL` | `gpt-5.4-mini` | OpenAI-specific model override when `WAYLOG_LLM_MODEL` is unset |
| `OPENAI_API_BASE` | OpenAI API default | OpenAI-specific API base URL override |

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
| `OTLP_ENABLED` | `true` | Enable OTLP trace ingest over HTTP and gRPC |
| `OTLP_GRPC_ADDR` | `:4317` | OTLP/gRPC trace receiver listen address. Set empty to disable the gRPC receiver |
| `MAX_BODY_BYTES` | `1048576` (1 MB) | Max body size for `/v1/events`, `/v1/otlp/v1/traces`, and OTLP/gRPC receive messages |
| `READ_HEADER_TIMEOUT` | `5s` | HTTP read header timeout |
| `READ_TIMEOUT` | `10s` | HTTP read timeout |
| `WRITE_TIMEOUT` | `10s` | HTTP write timeout |
| `IDLE_TIMEOUT` | `120s` | HTTP idle timeout |
| `CORS_ORIGIN` | `*` | Allowed CORS origin for read APIs |

## CLI

The `waylog` CLI calls the running ingest server's v2 read APIs. The server must run with `WAYLOG_V2_READS=true`.

| Variable | Default | Purpose |
|---|---|---|
| `INGEST_ADDR` | `http://localhost:8080` | CLI target ingest base URL. Bare `host:port` values are normalized to `http://host:port` |
| `WAYLOG_READ_KEY` | — | Read-scope API key sent as `Authorization: Bearer <key>` |
| `WAYLOG_CLI_TIMEOUT` | `5s` | Per-request CLI HTTP timeout |

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
| `WAYLOG_SIGNAL_RETENTION` | `72h` | Production-context signal retention. Must be positive. `/v1/signals` requires `SQLITE_PATH` |
| `WAYLOG_INCIDENTS_ENABLED` | `true` | Enable the v2.1 incident engine when `SQLITE_PATH` is set and `WAYLOG_V2_READS=true` |
| `WAYLOG_INCIDENT_TICK_INTERVAL` | `30s` | Incident engine evaluation interval |
| `WAYLOG_INCIDENT_WINDOW` | `10m` | Current error-family spike window |
| `WAYLOG_INCIDENT_MIN_COUNT` | `5` | Minimum current-window failures needed to open an incident |
| `WAYLOG_INCIDENT_MIN_LIFT` | `3.0` | Minimum current-vs-baseline lift when the family already exists in the baseline window |
| `WAYLOG_INCIDENT_RESOLVE_AFTER` | `2m` | Time without renewed matching failures before a recovering incident resolves |
| `WAYLOG_DEPLOY_CORRELATION_WINDOW` | `15m` | Window used to attach deploy signals and deployment records as incident evidence |
| `WAYLOG_INCIDENT_SAMPLE_LIMIT` | `5` | Maximum persisted sample traces per incident |
| `WAYLOG_REBUILD_INCIDENTS_ON_START` | `false` | Rebuild non-resolved incident rows at startup from the schema-2.0 WAL hot window plus signals |
| `WAYLOG_INCIDENT_REBUILD_MAX_EVENTS` | `250000` | Safety cap for startup incident rebuild replay |
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
| `GRAPH_UI` | `false` | Enable optional graph topology endpoint `/v1/graph/topology` |
| `WAYLOG_V2_READS` | `false` | Route v2 read endpoints to the schema-2.0 recent index |
| `CAUSAL_ENABLED` | `false` | Enable shadow-mode causal inference |
| `CAUSAL_INTERVAL` | `30s` | Causal inference ticker interval |
| `HAPPY_SAMPLE_RATE_PCT` | `2` | Success-event sampling rate. Set `100` in dev profiles |
| `MCP_STDIO` | — | Set to `1` to run MCP stdio server instead of REPL |

## Schema-2.0 reference demo

`make demo` sets these automatically for the detached local showcase. `make micro-demo` uses the same services in foreground debug mode.

| Variable | Default | Purpose |
|---|---|---|
| `INGEST_URL` | `http://localhost:8080` | SDK delivery base URL used by the demo services |
| `INGEST_ADDR` | `127.0.0.1:8080` in `make demo`; `:8080` in `make micro-demo` | Ingest server listen address |
| `WAYLOG_WRITE_KEY` | `demo` | Write-scope key used by the demo SDK emitters |
| `WAYLOG_READ_KEY` | `demo` | Read-scope key used by the printed CLI commands |
| `DASHBOARD_AUTH` | `off` in `make demo`; `key:demo` in `make micro-demo` | Dashboard auth mode for the local demo surface |
| `WAYLOG_V2_READS` | `true` in demo scripts | Enables v2 read APIs required by `waylog errors/explain/blast` |

The embedded `/ui` dashboard is a schema-2.0 triage surface and renders a setup message unless `WAYLOG_V2_READS=true`.

## Dashboard links

Optional external links rendered in the dashboard header. Hidden if empty.

| Variable | Purpose |
|---|---|
| `PROMETHEUS_URL` | Link to Prometheus UI |
| `GRAFANA_URL`    | Link to Grafana UI |

## Profiles

Pre-baked `.env` files live in [`deploy/`](../deploy):

- `deploy/dev.env` — 100% sampling, optional graph endpoints on, causal on, verbose logging
- `deploy/prod.env` — 5% happy-path sampling, graph UI off by default, tighter retention

Use with `make docker-dev` or `make docker-prod`.
