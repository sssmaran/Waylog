# Waylog SDK Contract

This document defines the language-agnostic contract for SDKs that emit Waylog
WideEvents. The current implementation contract is schema-2.0. Public release
surface naming is intentionally deferred until the final normalization slice.

For the machine-checkable schema, see [`schema/v2.0.json`](schema/v2.0.json).

## Core Rules

- The SDK is emit-only. Reads happen through the ingest server, CLI, dashboard,
  or agent/tool APIs.
- Every emitted event must be valid schema-2.0 JSON before delivery.
- One request handled by one service emits one WideEvent.
- Request-scoped state flows through middleware context.
- Trace propagation uses W3C `traceparent`.
- Stable operator fields matter more than raw log volume: `service`, `trace_id`,
  `status`, `anchor`, `steps`, `logs`, and `fields` drive triage.

## Required Top-Level Fields

- `schema_version`: currently `"2.0"`
- `event_id`: UUID
- `ts_start`, `ts_end`: UTC timestamps
- `kind`: currently `http`
- `service`
- `env`
- `trace_id`: 32 lowercase hex chars
- `status`: `ok`, `error`, `timeout`, `partial`, `aborted`, or `suppressed`

Recommended fields:

- `duration_ms`
- `version`
- `span_id`
- `parent_span_id`
- `fields.http.method`
- `fields.http.route`
- `fields.http.status`
- `fields.user.id`, `fields.user.tier`, `fields.user.region`

## Failure Contract

Failed events should set:

- `status` to one of `error`, `timeout`, `partial`, or `aborted`
- `anchor.step` to the first operator-meaningful failing step
- `anchor.error_code` to a stable code such as `PMT_502`
- A matching failed item in `steps[]` with `status: "error"` and `error.code`
- `errors[]` with the same stable error code

Suppressed events use `status: "suppressed"`. They remain available through
direct event/trace/recent surfaces when explicitly requested, but are excluded
from error rollups and blast radius.

## Step Contract

`steps[]` is the primary explanation surface. Emit steps at the level an
operator would reason about:

```text
cart.validate → db.load_cart → inventory.reserve → payment.charge → order.commit
```

Each step should include:

- `name`
- `start_ms`
- `duration_ms`
- `status`
- `span_id` when the step represents a downstream call or child span
- `downstream.service` and `downstream.endpoint` for real service calls
- `error.code` and `error.reason` when the step fails

## Logs

`logs[]` should contain milestone logs useful inside `explain`, not every
application log line. Recommended levels are `info`, `warn`, and `error`.

Good examples:

- `cart validated`
- `cart loaded`
- `inventory reserved`
- `retrying payment`
- `upstream gateway 5xx`
- `order committed`

## Transport

The primary SDK transport is HTTP ingest:

```text
POST /v1/events
Content-Type: application/json
Authorization: Bearer $WAYLOG_WRITE_KEY
```

OTLP/HTTP traces can also be sent to:

```text
POST /v1/otlp/v1/traces
Content-Type: application/x-protobuf
```

Kafka and bridge paths are optional/internal integration surfaces, not the
default demo or SDK contract.

## Minimal Example

```json
{
  "schema_version": "2.0",
  "event_id": "11111111-1111-4111-8111-111111111111",
  "ts_start": "2026-05-04T10:00:00Z",
  "ts_end": "2026-05-04T10:00:00.042Z",
  "duration_ms": 42,
  "kind": "http",
  "service": "checkout",
  "env": "prod",
  "trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "span_id": "bbbbbbbbbbbbbbbb",
  "status": "error",
  "anchor": {
    "step": "payment.charge",
    "error_code": "PMT_502",
    "kind": "downstream"
  },
  "steps": [
    {"name": "cart.validate", "start_ms": 0, "duration_ms": 1, "status": "ok"},
    {
      "name": "payment.charge",
      "span_id": "cccccccccccccccc",
      "start_ms": 7,
      "duration_ms": 35,
      "status": "error",
      "downstream": {"service": "payment", "endpoint": "/charge", "kind": "http"},
      "error": {"code": "PMT_502", "reason": "upstream gateway 5xx"}
    }
  ],
  "logs": [
    {"ts_offset_ms": 40, "level": "warn", "msg": "retrying payment"},
    {"ts_offset_ms": 42, "level": "error", "msg": "upstream gateway 5xx"}
  ],
  "fields": {
    "http": {"method": "POST", "route": "/checkout", "status": 502},
    "user": {"id": "u-123", "tier": "standard", "region": "us-east-1"}
  },
  "errors": [{"code": "PMT_502", "reason": "upstream gateway 5xx"}]
}
```

## Implementation Checklist

- Add HTTP middleware to create request context and emit exactly one event.
- Parse and propagate W3C `traceparent`.
- Populate service, env, route, status, trace, and user fields.
- Wrap meaningful business/downstream operations in steps.
- Mark the first failing operator step as `anchor`.
- Validate before emit.
- Use non-blocking delivery with safe shutdown.
