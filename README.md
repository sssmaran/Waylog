<div align="center">
  <img width="461" height="129" alt="Waylog" src="https://github.com/user-attachments/assets/fdec0a1b-055a-47e3-b52c-cf1cf80fe373" />

  <p>
    <strong>Production incident triage you can hash, cite, and hand to an agent.</strong><br>
    Drop-in SDKs (Go, TypeScript) or OTLP HTTP/gRPC. Deterministic. Single Go binary.
  </p>

  <p>
    <code>schema 2.0 WideEvents</code> · <code>signal-correlated incidents</code> · <code>cited operator reports</code> · <code>rollup-correct root cause</code> · <code>agent-native</code>
  </p>

  <p>
    <code>alpha</code> · <code>Apache-2.0</code> · <code>go 1.24+</code> · <code>single-node</code> · <code>OTLP-compatible</code> · <code>MCP-native</code> · <code>LLM-optional</code>
  </p>
</div>

> **Public alpha for single-node production-style incident triage.** APIs may break before 1.0.

---

## Try it in 60 seconds

```bash
git clone https://github.com/sssmaran/WaylogCLI
cd WaylogCLI
make demo
```

Then open <http://localhost:9081/demo> and click **Run proof loop**.

You'll see the full v2.1 flow run end-to-end: an external alert is accepted, a payment-failure burst spikes an error family, the spike detector opens an incident, the cause is classified as `dependency`, a triage report is built across four surfaces (CLI, read endpoint, direct tool, plan template) and verified byte-stable, and Markdown / Slack / PagerDuty operator reports are rendered with citations to every alert, signal, and trace.

Stop with `make demo-stop`. No Docker. No Kafka. No bridge process. SQLite + a single Go binary.

---

## What Waylog is

Waylog turns failed requests and external alerts into **incidents with deterministic, cited triage reports** that humans and agents can both consume. Three things make it different:

- **Deterministic.** Every triage report has a `report_hash` that is byte-stable across CLI, REST read, direct tool call, and plan template — within a single engine tick. No LLM required to get an answer; LLMs are an optional UX layer over the same bytes.
- **Agent-native.** The same tool registry powers the CLI, MCP stdio, REST `/v1/tools/*`, and `/v1/plans/execute`. Agents read the exact bytes the human read. Built-in triage plan template, idempotency keys, structured envelopes.
- **Drop-in.** Go SDK (`net/http`, chi, gin, echo), TypeScript SDK (Express, Hono, Next.js, NestJS), or OTLP HTTP / gRPC. Use what you already have. Single binary + embedded SQLite. No Docker required.

```text
  trace 7f3a2b9c…   flow=purchase   user=standard   region=us-east-1

  api-gateway        502   GW_DOWNSTREAM         14 ms   (root)
      └─ checkout    502   CHK_DOWNSTREAM         9 ms
          └─ db      200   —                      3 ms
          └─ payment 502   PMT_502                5 ms   ← first failure

  blast radius:    12 requests · 8 users · 4 services
  incident:        inc_a43c189fc63eff31   (cause=dependency · confidence=high)
  report_hash:     sha256:1ed7c21b…       (stable across cli / read / tool / plan)
```

Root-cause rollups count the originating failure **once**, not once per propagated hop. The same hash answers "what failed" identically whether the question came from a terminal, a webhook, or an LLM tool call.

---

## The incident loop

The v2.1 product in one paragraph: services emit WideEvents (or push OTel spans). External systems post production-context signals (deploys, dependency health, runtime events) and alerts (Alertmanager, Grafana, PagerDuty webhooks, or Waylog-native JSON). When an error family spikes, Waylog opens an **incident**, correlates the signals and alerts that overlap its window, classifies the cause deterministically (`deploy | app | dependency | runtime | unknown`), and exposes a cited **TriageReport** to humans (CLI, dashboard) and agents (REST, plan template, MCP) — same bytes, same hash.

```bash
# 1. Drive a real failure through the demo stack
curl -X POST http://localhost:9081/purchase \
  -H 'Content-Type: application/json' \
  -d '{"sku":"X1","scenario":"payment_502"}'

# 2. List active incidents
waylog incidents

# 3. Get the cited triage report — and verify hash agreement across surfaces
waylog triage inc_a43c189fc63eff31 --snapshot
curl -H "Authorization: Bearer $WAYLOG_AGENT_KEY" \
  -d '{"incident_id":"inc_a43c189fc63eff31","snapshot":true}' \
  http://localhost:8080/v1/tools/triage_incident

# 4. Render an operator report (Markdown / Slack Block Kit / PagerDuty note)
curl -H "Authorization: Bearer $WAYLOG_READ_KEY" \
  "http://localhost:8080/v1/triage/inc_a43c189fc63eff31/report?format=slack"
```

> **Alerts correlate; they do not create incidents.** Incidents are opened by the spike detector. Alerts and signals attach as evidence and shape the deterministic cause classification.

---

## Capture: send Waylog your traces

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

ESM-only, Node 18+. Standalone core plus framework entrypoints: `@waylog/sdk/express`, `@waylog/sdk/hono`, `@waylog/sdk/next`, `@waylog/sdk/nest`. Full examples in [`docs/sdk-examples.md`](docs/sdk-examples.md).

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

Middleware adapters for `net/http`, chi, gin, and echo are in [`docs/sdk-examples.md`](docs/sdk-examples.md). The recommended path is framework middleware plus `waylog.From(ctx)` / `useLogger(...)` inside handlers — low-level `Begin` / `Finalize` / `setField` APIs are for adapter authors.

The Go and TypeScript SDKs are kept in parity — wire format, config, signals, and public API. The audited matrix (and the documented idiomatic gaps) is in [`docs/sdk-parity.md`](docs/sdk-parity.md).

### OTLP / OpenTelemetry

Point your existing OTel collector at Waylog. Both protocols, same conversion path, same downstream views.

```yaml
exporters:
  otlphttp/waylog:
    endpoint: http://localhost:8080/v1/otlp/v1/traces
    headers:
      authorization: "Bearer ${WAYLOG_WRITE_KEY}"
  otlp/waylog:
    endpoint: localhost:4317
    headers:
      authorization: "Bearer ${WAYLOG_WRITE_KEY}"
```

Sample collector config: [`examples/otel-collector/`](examples/otel-collector/). Only traces are accepted; OTLP logs and metrics are not shipping. Bind `OTLP_GRPC_ADDR=127.0.0.1:4317` for single-host installs that don't need cross-host collectors.

**Deploy correlation works OTel-only.** When spans carry `service.version` and the version changes for a `(service, env)` pair, Waylog auto-registers a deployment — no deploy webhook needed for incidents to classify `cause=deploy`.

**Auth.** Both endpoints require `WAYLOG_WRITE_KEY` when `WAYLOG_PROFILE=prod`; the server refuses to boot with unauthenticated OTLP in prod. `make demo` runs unauthenticated by design.

### Local ingest only (no demo services)

```bash
make ingest
```

Runs only the ingest server. Point your own services at it via SDK or OTLP. Full env reference: [`docs/env.md`](docs/env.md).

---

## Operate: CLI, dashboard, agents

### CLI

```bash
./ingest                                 # v2 reads are on by default

waylog capabilities                      # diagnose server config / profile / feature flags
waylog incidents                         # active incidents, deterministic order
waylog incident <incident_id> --snapshot # full detail with frozen sample traces
waylog triage   <incident_id>            # cited triage report
waylog errors   --window 15m             # top error families
waylog explain  <trace_id>               # first observable failing step
waylog blast    checkout:payment.charge:PMT_502 --window 15m
waylog recent   --limit 5
waylog event    <event_id>
waylog trace    <trace_id>
waylog search   PMT_502 --window 1h
```

`waylog capabilities` is intentionally ungated so it can diagnose server setup; other verbs require `v2_reads.enabled=true` (the default). Defaults: `INGEST_ADDR`, `WAYLOG_READ_KEY`, `WAYLOG_CLI_TIMEOUT`. Add `--json` to any verb for machine-readable output.

### Self-contained first-run

```bash
make first-run        # or, with crux on PATH: crux first-run
```

`crux first-run` launches a throwaway ingest server, drives a real
checkout→payment failure burst through the SDK so the spike detector opens a
**real** incident, then prints the deterministic triage report with its
`report_hash` and `evidence_fingerprint`. No Docker, no Kafka — SQLite plus a
single binary against a temp data dir. The server stays up afterward so you can
open `/ui/` and run `crux incidents`.

### Interactive shell

```bash
make build-crux
./crux
```

`crux` opens a lightweight incident-triage shell with `help`, `status`, `incidents`, `open <id>`, `triage <id>`, `blast <service>:<step>:<code>`, `explain <trace_id>`, and `exit`. With arguments, it delegates to the same command library as `waylog`, so `crux incidents` and `waylog incidents` share behavior.

### Dashboard

Embedded Geist UI at <http://localhost:8080/ui/>. Uses the dashboard session cookie for read-scope auth and runs against the v2 reader.

- `#/errors` — top error families over `/v1/errors`
- `#/incident/<id>` — incident evidence and next checks over `/v1/incidents/{id}`
- `#/explain/<id>` — first observable failing step over `/v1/traces/story`
- `#/blast/<key>` — impact panel over `/v1/blast_radius`
- recent-request stream from `/v1/traces/recent`, polled every 5 s

No Chart.js, Cytoscape, topology-first UI, Ask panel, deploy diff, or large dashboard charts. Just the triage loop.

### Agent surface

Four deterministic tools, exposed identically through CLI, REST `/v1/tools/{name}`, MCP stdio, and plan execution. Same idempotency keys, same structured envelopes, same bytes.

| Tool                   | Answers                                                                                      |
| ---------------------- | -------------------------------------------------------------------------------------------- |
| `triage_incident`      | Structured TriageReport for an open incident (blast + first failure + signals + next checks) |
| `render_triage_report` | Markdown, Slack Block Kit JSON, or PagerDuty note from a TriageReport                        |
| `explain_request`      | Trace story (per-step path, anchor, downstream) for a given `trace_id`                       |
| `blast_radius`         | How many requests, users, and services does this error family touch in the window?           |

```bash
# Direct tool call
curl -X POST http://localhost:8080/v1/tools/blast_radius \
  -H "Authorization: Bearer $WAYLOG_AGENT_KEY" \
  -d '{"service":"payment-service","step":"charge","error_code":"PMT_502","window":"10m"}'

# Built-in triage plan template — same hash as the CLI/read/tool surfaces
curl -X POST http://localhost:8080/v1/plans/execute \
  -H "Authorization: Bearer $WAYLOG_AGENT_KEY" \
  -d '{"template":"triage","params":{"incident_id":"inc_...","snapshot":true}}'
```

Plans execute deterministically server-side with SSE progress on `/v1/stream/plans/{id}`. Full schemas: `GET /v1/tools` or [`docs/openapi.yaml`](docs/openapi.yaml).

### MCP

```bash
make ingest-mcp     # MCP_STDIO=1
```

Same registry, same idempotency keys. Plugs into Claude, Cursor, and other MCP clients.

### External alerts

```bash
curl -X POST http://localhost:8080/v1/alerts \
  -H "Authorization: Bearer $WAYLOG_WRITE_KEY" \
  -d @grafana-webhook.json
```

`POST /v1/alerts` accepts Waylog-normalized JSON plus Alertmanager, Grafana, and PagerDuty webhook payloads. Accepted alerts are stored as `type=alert` signals and **correlated** with active incidents when possible — alerts do not create incidents. The matching alert evidence then appears in cited Markdown / Slack / PagerDuty triage reports.

---

## Architecture

```text
   Go / TS services (SDK)               OTel collectors
              │                                │
   schema-2.0 WideEvents                OTLP HTTP / gRPC
              ╰──────────────┬─────────────────╯
                             ▼
                      ingest server
              ┌──────────────┴──────────────┐
              │                              │
   event log (append-only WAL,       SQLite cold store
       source of truth)              (events · deployments ·
              │                       signals · incidents)
              ▼
   v2 reader (in-memory hot
   index over schema-2.0 WAL)
              │
              ├──▶ /v1/errors · /v1/blast_radius · /v1/traces/* · /v1/events/*
              ├──▶ incidents engine → /v1/incidents/* · /v1/triage/*
              ├──▶ /ui dashboard          (Geist, no vendored chart/topology)
              ├──▶ /v1/tools/*            (four v1.0 agent tools)
              ├──▶ /v1/plans/execute      (server-side plan execution + SSE)
              └──▶ waylog CLI · MCP
```

- **Single binary** plus embedded SQLite. No Docker, no Kafka, no bridge.
- **WAL is source of truth.** Crash → replay on next boot rebuilds the v2 reader's hot index.
- **v2 reader is the only hot path.** Pruned every tick to enforce `GRAPH_HOT_WINDOW` (default 24h).
- **`report_hash` excludes `generated_at`, `plan_run_id`, and itself.** Same upstream state → same bytes across every surface. **`evidence_fingerprint`** complements it: stable across ticks until the incident's evidence set changes, so operators and agents can cite a triage answer durably.
- **OTLP path reuses the same WAL and projector** as the SDK path. No separate ingestion plane.

Durability model, retention, merge semantics, readiness policy, and counter buffer details: [`docs/internals.md`](docs/internals.md). Scale ceiling and how to tune within it: [`docs/scale-and-limits.md`](docs/scale-and-limits.md). Full HTTP contract: [`docs/openapi.yaml`](docs/openapi.yaml).

---

## Auth & profiles

Three independent scoped keys. The dashboard never holds the agent key.

| Key                | Protects                                                    |
| ------------------ | ----------------------------------------------------------- |
| `WAYLOG_WRITE_KEY` | `/v1/events`, OTLP HTTP + gRPC, `/v1/signals`, `/v1/alerts` |
| `WAYLOG_READ_KEY`  | Read APIs, dashboard session                                |
| `WAYLOG_AGENT_KEY` | `/v1/tools/*`, `/v1/ask`, `/v1/plans/*`                     |

`WAYLOG_API_KEY` is a legacy alias for the write scope. `ParseConfig` validates the auth matrix at startup and refuses to boot with an unsafe combination.

`WAYLOG_PROFILE` controls auth strictness. The current profile is reported on `/v1/capabilities`.

| Profile | Use case                    | Defaults                                                                                              |
| ------- | --------------------------- | ----------------------------------------------------------------------------------------------------- |
| `demo`  | `make demo` showcase        | All endpoints open. **Not safe to expose to a network.**                                              |
| `dev`   | Local development (default) | Open OTLP, optional read auth                                                                         |
| `prod`  | Real deployments            | Refuses to boot without all three scoped keys **and** without `WAYLOG_WRITE_KEY` when OTLP is enabled |

Set `WAYLOG_PROFILE=prod` for any deployment that crosses a trust boundary.

---

## Try every claim locally

```bash
make demo                # one-shot local stack (no Docker)
make demo-acceptance     # 15-check gate over CLI + browser proof
make proof-loop          # full alert → incident → triage → cited reports loop
make rca-scorecard       # cold-start latency + measured report_hash_stable
make rollup-comparison   # rollup-correct counts vs naive propagated counts
make demo-stop
```

`make proof-loop` writes shareable artifacts to `./data/demo-state/proof/`:

- `triage.json` — the TriageReport from the read endpoint
- `report.md`, `slack.json`, `pagerduty.txt` — the same triage rendered for three operator surfaces
- `rollup-comparison.txt` — root-cause counts next to naive propagated counts
- `scorecard.json` — measured `report_hash_stable`, `triage_latency_ms`, `scenario` (`cold-demo` for `rca-scorecard`, `warm-demo` chained from `proof-loop`), inflation-avoided count, and the `report_hash` itself

The browser has the same flow at <http://localhost:9081/demo> → **Run proof loop**.

> Artifacts are local-only. Alert payloads include the demo write key (`Bearer demo`). `data/demo-state/` is gitignored.

---

## Development

```bash
make build              # core binaries
make build-examples     # demo services
make fmt vet test       # checks
make test-race          # race detector
make ts-test            # TypeScript SDK vitest suite
make ci                 # fmt + vet + test-race + test-sdk + ts-test + doc-link + rollup-contract
```

Full env-var reference: [`docs/env.md`](docs/env.md). Reproducible demo gate: `make demo-acceptance`.

---

## What's new in v2.1

- **Signal-correlated incident engine** at `/v1/incidents/*` with deterministic cause classification (`deploy | app | dependency | runtime | unknown`).
- **Deterministic TriageReport** (`triage.v1`) with stable per-tick `report_hash` across CLI, read endpoint, direct tool, and plan template.
- **Cross-tick `evidence_fingerprint`** on every triage report — hashes the evidence identity set (incident + signal + alert + runtime + trace IDs), stable across engine ticks until the evidence changes, so a triage answer can be cited durably even as `report_hash` rolls with the window.
- **Alert intake** at `POST /v1/alerts` for Waylog-native, Alertmanager, Grafana, and PagerDuty webhooks. Alerts correlate with active incidents; they do not create incidents.
- **Cited operator reports** rendered as Markdown, Slack Block Kit, or PagerDuty notes via `GET /v1/triage/{id}/report`.
- **OTLP/gRPC trace receiver** on `OTLP_GRPC_ADDR` (default `:4317`).
- **OTLP deploy correlation** — a `service.version` change on incoming spans auto-registers a deployment, so incidents classify `cause=deploy` with no SDK and no deploy webhook.
- **Per-key rate limiting + backpressure** — token-bucket throttling on write/read/agent scopes (`WAYLOG_RATE_LIMIT_*_RPS`, off by default, on in the `prod` profile) returning `429` + `Retry-After`; WAL-failure 503s also send `Retry-After`.
- **Automatic resolved-incident retention** — a janitor prunes resolved incidents older than `WAYLOG_INCIDENT_RETENTION` (default 168h); active and recovering incidents are never touched.
- **`crux first-run`** — one command spins up ingest, drives a real failure burst, opens a real incident, and prints the cited deterministic report.
- **Provider-neutral Ask** configuration: `gemini`, `anthropic`, `openai`, or `none`. All deterministic surfaces work with no LLM configured.
- **`WAYLOG_PROFILE=demo|dev|prod`** gates auth defaults; `prod` hard-fails on unsafe configs.
- **`/v1/insight`** retained as a compat shim returning the top active incident. New clients should use `/v1/incidents/*`.

---

## Status

Public alpha for single-node production-style incident triage. APIs may break before 1.0.

**Shipped**

- Go SDK v2 (`net/http`, chi, gin, echo) and TypeScript SDK v2 (`@waylog/sdk`, ESM, Node 18+, standalone core, Express, Hono, Next.js, NestJS)
- OTLP HTTP at `/v1/otlp/v1/traces` and OTLP/gRPC at `OTLP_GRPC_ADDR` (traces only), with `service.version`-change deploy auto-registration for OTel-only deploy correlation
- Durable ingest with WAL + replay
- Schema-2.0 v2 reader powering all hot read APIs (`/v1/errors`, `/v1/blast_radius`, `/v1/traces/*`, `/v1/events/*`)
- SQLite cold store (events, deployments, signals, incidents)
- Signal-correlated incident engine with stable IDs, deterministic classification, startup hot-window rebuild from the schema-2.0 WAL, and an automatic resolved-incident retention janitor (`WAYLOG_INCIDENT_RETENTION`)
- Alert intake from four webhook formats, stored as signals and correlated with active incidents
- Deterministic triage report with a per-tick `report_hash` across CLI / read endpoint / direct tool / plan template, plus a cross-tick `evidence_fingerprint` for durable citation
- Provider-neutral Ask configuration; deterministic CLI, tools, plans, triage, and MCP work with no LLM configured
- Four deterministic v1.0 agent tools (`explain_request`, `blast_radius`, `triage_incident`, `render_triage_report`)
- Agent-native REST with idempotency and structured envelopes
- `crux first-run` self-contained demo (launch → real failure burst → real incident → cited deterministic report)
- MCP stdio, embedded Geist dashboard
- Scoped auth (write / read / agent) with startup validation and `WAYLOG_PROFILE=prod` hard-fail, plus per-key rate limiting with `429` + `Retry-After` backpressure

**Planned**

- OTLP logs and metrics
- Python SDK
- Mintlify docs site

---

## Known limitations

- **Public alpha.** APIs may break before 1.0. Not production-ready. Not HA.
- **Triage report hash is stable per tick, not forever.** `report_hash` changes when the underlying recent-index window changes (≈30 s default) — use it as a short-window dedup key proving all four surfaces returned the same bytes. For citations that survive across ticks, use `evidence_fingerprint`: it hashes only the evidence identity set (incident + signal + alert + runtime + trace IDs) and changes exactly when evidence is attached (see `docs/adr/0002-evidence-fingerprint.md`).
- **Alerts correlate; they do not create incidents.** Incidents are opened by the spike detector. The alert path is for routing context, not paging primitives.
- **Stale `active` rows after long downtime.** If the WAL has rolled past an incident's `started_at` and `WAYLOG_REBUILD_INCIDENTS_ON_START=true`, the engine transitions only the stale rows to `recovering` on next start; they resolve after `WAYLOG_INCIDENT_RESOLVE_AFTER` without new evidence.
- **Single-node only.** No HA, no clustering, no multi-tenant.
- **SQLite cold store** fits demos and small deployments — see [Scale & limits](docs/scale-and-limits.md) for the ceiling and how to tune within it. Postgres is not shipping.
- **OTLP supports traces only.** Logs and metrics are not shipping yet.
- **Only Go and TypeScript SDKs today.** Python / Java / Ruby are not available.
- **No outbound paging.** Waylog accepts external alerts and renders operator reports; it does not page.
- **No multi-tenancy.** One instance = one trust boundary.
- **No full log search, Slack/PagerDuty automation, RBAC/SSO, or automatic remediation.**
- **Incident cause classification is heuristic and deterministic.** Intentionally explainable, not ML-based.

---

## Documentation

| File                                                         | What's in it                                                                             |
| ------------------------------------------------------------ | ---------------------------------------------------------------------------------------- |
| [`docs/env.md`](docs/env.md)                                 | Full env-var reference (auth, profiles, retention, OTLP, incident engine, LLM providers) |
| [`docs/sdk-examples.md`](docs/sdk-examples.md)               | Copy-paste SDK examples for every supported framework                                    |
| [`docs/waylog-sdk-contract.md`](docs/waylog-sdk-contract.md) | WideEvent schema and validation rules                                                    |
| [`docs/openapi.yaml`](docs/openapi.yaml)                     | Full HTTP contract                                                                       |
| [`docs/internals.md`](docs/internals.md)                     | Durability model, retention, merge semantics, readiness policy, counter buffer           |

---

## Project layout

```
cmd/         executable binaries (ingest, waylog, ...)
pkg/         public SDK importable by external services
internal/    private implementation (auth, incidents, triage, ingest, ...)
examples/    demo services + collector config + microdemo
scripts/     demo + proof + ci helpers
docs/        reference and contracts
```

---

## License

[Apache License 2.0](LICENSE). You may use, modify, and distribute this software under its terms, which include an explicit patent grant.
