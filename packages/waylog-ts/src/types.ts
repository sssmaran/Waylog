// WideEvent shape mirrors pkg/event/event.go (schema 1.1). Fields that map to
// Go `omitempty` are optional here so the wire payload matches exactly.

export const SCHEMA_VERSION = "1.1";

export interface UserContext {
  id: string;
  tier: string;
  region: string;
  vip: boolean;
}

export interface RequestContext {
  trace_id: string;
  span_id?: string;
  parent_span_id?: string;
  http_method?: string;
  route_template?: string;
  flow: string;
  feature_flags: string[];
  correlation_id?: string;
  attempt?: number;
  transport_kind?: string;
}

export interface SystemContext {
  service: string;
  version: string;
  deployment_id: string;
  env: string;
  downstream_service?: string;
  caller_service?: string;
}

export interface OutcomeContext {
  success: boolean;
  status_code: number;
  kind: string;
}

export interface ErrorContext {
  code: string;
  path?: string;
  message: string;
  reason?: string;
}

export interface RetryContext {
  of?: number;
  previous_attempt_id?: string;
}

export interface MetricsContext {
  latency_ms: number;
}

export interface WideEvent {
  schema_version: string;
  event_name: string;
  timestamp: string;
  user: UserContext;
  request: RequestContext;
  system: SystemContext;
  outcome: OutcomeContext;
  error?: ErrorContext;
  metrics: MetricsContext;
  parent_request_id?: string;
  metadata?: Record<string, unknown>;
  retry?: RetryContext;
}

export interface WaylogConfig {
  endpoint: string;
  service: string;
  version?: string;
  env?: string;
  apiKey?: string;
  batchSize?: number;
  flushIntervalMs?: number;
  queueMax?: number;
  fetch?: typeof fetch;
  now?: () => Date;
}

export interface LoggerOptions {
  traceId?: string;
  spanId?: string;
  parentSpanId?: string;
  flow?: string;
  user?: Partial<UserContext>;
  httpMethod?: string;
  routeTemplate?: string;
  parentRequestId?: string;
}

// SetFields accepts partial sub-contexts so callers can patch one field at a
// time (e.g. `set({ user: { tier: "pro" } })`) without re-specifying the
// whole sub-context.
export interface SetFields {
  user?: Partial<UserContext>;
  request?: Partial<RequestContext>;
  system?: Partial<SystemContext>;
  outcome?: Partial<OutcomeContext>;
  metrics?: Partial<MetricsContext>;
  metadata?: Record<string, unknown>;
  error?: ErrorContext;
  retry?: RetryContext;
  event_name?: string;
  timestamp?: string;
  parent_request_id?: string;
}

export interface RequestLogger {
  set(fields: SetFields): RequestLogger;
  error(err: ErrorContext | Error | string): RequestLogger;
  emit(outcome?: Partial<OutcomeContext>): void;
  emitted(): boolean;
  traceparent(): string;
}
