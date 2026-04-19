import { isErrorContext } from "./error.js";
import { Transport } from "./transport.js";
import {
  SCHEMA_VERSION,
  type ErrorContext,
  type LoggerOptions,
  type OutcomeContext,
  type RequestLogger,
  type SetFields,
  type WaylogConfig,
  type WideEvent,
} from "./types.js";

// 32/16-hex random IDs. W3C traceparent version is fixed at "00".
function randHex(bytes: number): string {
  const buf = new Uint8Array(bytes);
  crypto.getRandomValues(buf);
  return Array.from(buf, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function newTraceId(): string { return randHex(16); }
export function newSpanId(): string { return randHex(8); }

// parseTraceparent extracts the trace-id and parent-id from a W3C
// `00-<trace>-<span>-<flags>` header. Returns undefined on malformed input so
// callers fall back to generating fresh IDs.
export function parseTraceparent(h: string | undefined | null): { traceId: string; spanId: string } | undefined {
  if (!h) return undefined;
  const parts = h.trim().split("-");
  if (parts.length !== 4) return undefined;
  const [version, traceId, spanId] = parts;
  if (version !== "00") return undefined;
  if (!/^[0-9a-f]{32}$/.test(traceId!) || !/^[0-9a-f]{16}$/.test(spanId!)) return undefined;
  return { traceId: traceId!, spanId: spanId! };
}

export interface Logger {
  withRequest(opts?: LoggerOptions): RequestLogger;
  flush(): Promise<void>;
  close(): Promise<void>;
  droppedCount(): number;
}

// startRequest is the shared setup middlewares run for every incoming request:
// parse inbound traceparent, create a RequestLogger, and return both the logger
// and the outbound traceparent header so the framework can set it however it
// prefers (Express res.setHeader vs Hono c.res.headers.set).
export function startRequest(
  logger: Logger,
  ctx: { method?: string; route?: string; traceparentHeader?: string },
): { log: RequestLogger; traceparent: string } {
  const tp = parseTraceparent(ctx.traceparentHeader);
  const log = logger.withRequest({
    traceId: tp?.traceId,
    parentSpanId: tp?.spanId,
    httpMethod: ctx.method,
    routeTemplate: ctx.route,
  });
  return { log, traceparent: log.traceparent() };
}

class RequestLoggerImpl implements RequestLogger {
  private event: WideEvent;
  private wasEmitted = false;
  private readonly startedAt: number;
  private readonly transport: Transport;

  constructor(base: WideEvent, transport: Transport, startedAt: number) {
    this.event = base;
    this.transport = transport;
    this.startedAt = startedAt;
  }

  set(fields: SetFields): RequestLogger {
    const e = this.event;
    if (fields.user) e.user = { ...e.user, ...fields.user };
    if (fields.request) e.request = { ...e.request, ...fields.request };
    if (fields.system) e.system = { ...e.system, ...fields.system };
    if (fields.outcome) e.outcome = { ...e.outcome, ...fields.outcome };
    if (fields.metrics) e.metrics = { ...e.metrics, ...fields.metrics };
    if (fields.metadata) e.metadata = { ...(e.metadata ?? {}), ...fields.metadata };
    if (fields.error !== undefined) e.error = fields.error;
    if (fields.retry !== undefined) e.retry = fields.retry;
    if (fields.event_name !== undefined) e.event_name = fields.event_name;
    if (fields.timestamp !== undefined) e.timestamp = fields.timestamp;
    if (fields.parent_request_id !== undefined) e.parent_request_id = fields.parent_request_id;
    return this;
  }

  error(err: ErrorContext | Error | string): RequestLogger {
    if (typeof err === "string") {
      this.event.error = { code: "UNKNOWN", message: err };
    } else if (isErrorContext(err)) {
      this.event.error = err;
    } else {
      this.event.error = { code: "UNKNOWN", message: err.message || "error" };
    }
    return this;
  }

  emit(outcome?: Partial<OutcomeContext>): void {
    if (this.wasEmitted) return;
    this.wasEmitted = true;
    if (outcome) this.event.outcome = { ...this.event.outcome, ...outcome };
    this.event.metrics.latency_ms = Math.max(0, Date.now() - this.startedAt);
    // event_name default: "<service>.request" on success, "<service>.error" on failure.
    if (!this.event.event_name) {
      const suffix = this.event.outcome.success ? "request" : "error";
      this.event.event_name = `${this.event.system.service}.${suffix}`;
    }
    this.transport.enqueue(this.event);
  }

  emitted(): boolean { return this.wasEmitted; }

  traceparent(): string {
    return `00-${this.event.request.trace_id}-${this.event.request.span_id ?? newSpanId()}-01`;
  }
}

export function createLogger(config: WaylogConfig): Logger {
  if (!config.service) throw new Error("createLogger: service is required");
  const transport = new Transport(config);
  const now = config.now ?? (() => new Date());

  return {
    withRequest(opts: LoggerOptions = {}): RequestLogger {
      const traceId = opts.traceId ?? newTraceId();
      const spanId = opts.spanId ?? newSpanId();
      const base: WideEvent = {
        schema_version: SCHEMA_VERSION,
        event_name: "",
        timestamp: now().toISOString(),
        user: {
          id: opts.user?.id ?? "",
          tier: opts.user?.tier ?? "",
          region: opts.user?.region ?? "",
          vip: opts.user?.vip ?? false,
        },
        request: {
          trace_id: traceId,
          span_id: spanId,
          parent_span_id: opts.parentSpanId,
          flow: opts.flow ?? "",
          feature_flags: [],
          http_method: opts.httpMethod,
          route_template: opts.routeTemplate,
        },
        system: {
          service: config.service,
          version: config.version ?? "",
          deployment_id: "",
          env: config.env ?? "",
        },
        outcome: { success: true, status_code: 200, kind: "http" },
        metrics: { latency_ms: 0 },
        parent_request_id: opts.parentRequestId,
      };
      return new RequestLoggerImpl(base, transport, Date.now());
    },
    flush: () => transport.flush(),
    close: () => transport.close(),
    droppedCount: () => transport.droppedCount(),
  };
}
