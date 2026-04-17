# Waylog SDK Contract (Language-Agnostic)

This document defines the **language-agnostic contract** for any Waylog SDK implementation.
If a service emits events that follow this contract, the ingest pipeline will accept them
and the graph will build correctly—regardless of implementation language.

## 1) Core Principles

- **Emit-only**: the SDK only emits events; it does not query or analyze.
- **Valid-by-construction**: every emitted event MUST pass `WideEvent.Validate()` rules.
- **Single event per request**:
  - Success → `<service>.request`
  - Failure → `<service>.error`
- **Context-first**: all request-scoped values come from HTTP middleware.

## 2) Required Fields (Must Always Be Set)

**Top-level**
- `schema_version` = `"1.0"`
- `event_name`
- `timestamp` (UTC)

**User**
- `user.id` (if unknown, use `"system"`)

**Request**
- `request.trace_id`

**System**
- `system.service`
- `system.env`

**Outcome**
- `outcome.status_code` (must never be 0)

**Failures**
- If `outcome.success=false`, `error` must be present and `error.code` must be non-empty.

## 3) Event Naming (Hard Rule)

- **Success**: `<service>.request`
- **Failure**: `<service>.error`

No dynamic values (paths, methods, IDs, status codes).

## 4) Outcome + Error Rules

- `outcome.kind = "http"`
- `outcome.success = (status_code < 500)` unless an error was explicitly recorded.
- When an error is recorded:
  - `outcome.success = false`
  - If `status_code < 400`, **upgrade to 500**
  - If `status_code` is 4xx or 5xx, **preserve**
- `error.code` must be stable (fallback to `"UNKNOWN"`)
- `error.message` should be short (no stack traces)

## 5) Trace Propagation (HTTP)

### Inbound (strict W3C)
- Primary: `traceparent`
- Optional legacy fallback (inbound only):
  - `x-trace-id`
  - `x-span-id`
  - `x-request-id` (only if none of the above exist)

Rules:
- Trace ID: **32 hex chars**
- Span ID: **16 hex chars**
- Accept uppercase; normalize to lowercase.
- Invalid headers are ignored → generate new trace.

### Outbound (W3C only)
- Inject `traceparent: 00-<trace_id>-<span_id>-01`
- Inject `x-waylog-service: <this_service>`

### Parent/Child
- `parent_span_id` is derived from inbound `traceparent` span.
- `span_id` is newly generated per service.

## 6) Context Fields

**SystemContext**
- `service` (required)
- `env` (required)
- `version` (recommended)
- `deployment_id` (recommended)
- `caller_service` (from inbound `x-waylog-service`, else `external`)
- `downstream_service` (best-effort via HTTP client wrapper)

**RequestContext**
- `trace_id`
- `span_id`
- `parent_span_id`
- `http_method` (recommended; auto-captured by SDK middleware from `r.Method`)
- `route_template` (recommended; auto-captured from `r.Pattern` or explicitly set via `WithRouteTemplate`)
- `flow` (optional)
- `feature_flags` (optional list)

**UserContext**
- `id` (required, default `system`)
- `tier`, `region`, `vip` (optional)

**MetricsContext**
- `latency_ms` (middleware timer)

## 7) Transport

**Primary:** Kafka (topic `wide_events`)
- JSON-encoded `WideEvent`
- Asynchronous + buffered
- Best-effort; must not block request path

**Alternatives for dev/test:**
- `NopTransport`
- `InMemoryTransport`

## 8) Emission Lifecycle (Reference Middleware)

1. **Inbound middleware**
   - Parse `traceparent`
   - Generate `span_id`
   - Start latency timer
   - Capture response status (default 200)
2. **Handler executes**
   - Application calls `Error(ctx, err)` on failure
   - If handler panics: middleware recovers, records `panic: <value>` as error, emits failure event, then re-panics
3. **Request end**
   - Build `WideEvent`
   - Validate
   - Enqueue to transport

## 9) JSON Example (Success)

```json
{
  "schema_version": "1.0",
  "event_name": "checkout-service.request",
  "timestamp": "2026-02-03T19:11:00Z",
  "user": {"id": "u123", "tier": "free", "region": "us", "vip": false},
  "request": {
    "trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "span_id": "bbbbbbbbbbbbbbbb",
    "parent_span_id": "cccccccccccccccc",
    "flow": "checkout_v2",
    "feature_flags": ["new_checkout"]
  },
  "system": {
    "service": "checkout-service",
    "env": "prod",
    "version": "1.9.2",
    "deployment_id": "deploy_2026_02_03",
    "caller_service": "frontend",
    "downstream_service": "payment-service"
  },
  "outcome": {"success": true, "status_code": 200, "kind": "http"},
  "metrics": {"latency_ms": 123}
}
```

## 10) JSON Example (Failure)

```json
{
  "schema_version": "1.0",
  "event_name": "checkout-service.error",
  "timestamp": "2026-02-03T19:11:00Z",
  "user": {"id": "u123"},
  "request": {
    "trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "span_id": "bbbbbbbbbbbbbbbb",
    "parent_span_id": "cccccccccccccccc"
  },
  "system": {"service": "checkout-service", "env": "prod"},
  "outcome": {"success": false, "status_code": 502, "kind": "http"},
  "error": {"code": "PMT_502", "message": "payment gateway failure"},
  "metrics": {"latency_ms": 450}
}
```

## 11) Implementation Checklist (Any Language)

- [ ] Add HTTP middleware to capture trace + status + latency
- [ ] Ensure `user.id` always set (default `system`)
- [ ] Enforce event naming rules
- [ ] Populate system fields (`service`, `env`, `version`, `deployment_id`)
- [ ] Validate before emit
- [ ] Emit to Kafka topic `wide_events`
- [ ] Do not emit if no request state
- [ ] Non-blocking transport + safe shutdown

---

If you want language-specific examples (Java/Spring, Node/Express, Python/FastAPI), we can add a companion doc with minimal glue code per framework.
