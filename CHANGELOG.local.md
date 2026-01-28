# Local Change Notes

## Trace Propagation (Production Tracing)

- Purpose: connect spans across services by propagating trace/span IDs via HTTP headers.
- Headers used: `X-Trace-Id` and `X-Span-Id`.
- Behavior: inbound requests create a new span; if a parent span is provided, it becomes the parent for the new span.

### Changes

- Added trace helpers and HTTP middleware for extracting/injecting trace context.
  - File: `internal/trace/trace.go`
  - Why: centralizes trace propagation logic (context + headers + middleware).
- Wrapped the checkout HTTP handler with trace middleware.
  - File: `cmd/checkout/main.go`
  - Why: ensures every incoming request gets a trace context.
- Updated checkout span emission to use propagated trace/span IDs and parent-child relationships.
  - File: `internal/checkout/handler.go`
  - Why: root spans now link to inbound parents, and child spans link to the root span.
