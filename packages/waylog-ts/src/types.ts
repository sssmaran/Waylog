export const SCHEMA_VERSION = "2.0";

export type Status = "ok" | "error" | "timeout" | "partial" | "aborted" | "suppressed";
export type StepStatus = "ok" | "error";
export type LogLevel = "info" | "warn" | "error";
export type Fields = Record<string, unknown>;

export interface WaylogConfig {
  service: string;
  env: string;
  version?: string;
  output?: NodeJS.WritableStream | ((line: string) => void);
  ingestUrl?: string;
  apiKey?: string;
  devMode?: boolean;
  maxSteps?: number;
  maxLogs?: number;
  maxRequestAgeMs?: number;
  maxBufferBytes?: number;
  maxInFlightBytes?: number;
  maxEventsPerSec?: number;
  batchMode?: boolean;
  redactor?: (fields: Fields) => Fields;
  fetch?: typeof fetch;
}

export interface Stats {
  activeRequests: number;
  eventsEmitted: number;
  eventsSuppressed: number;
  stepsDropped: number;
  logsDropped: number;
  bytesDroppedFromBuffer: number;
  bufferOverflows: number;
  reservedCodeRejections: number;
  suppressedThenFailed: number;
  lateCompletionAfterEmit: number;
  eventsDropped: number;
  deliveryFailures: number;
  eventsRejected: number;
  deprecatedSchemaResponses: number;
}

export interface Anchor {
  step: string;
  error_code: string;
  kind?: string;
}

export interface Downstream {
  service?: string;
  endpoint?: string;
  kind?: string;
}

export interface StepError {
  code?: string;
  reason?: string;
  cause?: string;
}

export interface Step {
  name: string;
  span_id?: string;
  start_ms: number;
  duration_ms: number;
  status: StepStatus;
  downstream?: Downstream;
  error?: StepError;
}

export interface Log {
  ts_offset_ms: number;
  level: LogLevel;
  msg: string;
  fields?: Fields;
}

export interface ErrorRef {
  code: string;
  reason?: string;
}

export interface WideEvent {
  schema_version: string;
  event_id: string;
  ts_start: string;
  ts_end: string;
  duration_ms: number;
  kind: "http";
  service: string;
  env: string;
  version?: string;
  trace_id: string;
  span_id: string;
  parent_span_id: string;
  status: Status;
  anchor?: Anchor;
  steps?: Step[];
  logs?: Log[];
  fields?: Fields;
  errors?: ErrorRef[];
}

export interface ErrorOpts {
  reason?: string;
  cause?: string;
}

export class WaylogError extends Error {
  readonly code: string;
  readonly reason?: string;
  readonly causeText?: string;

  constructor(code: string, opts: ErrorOpts = {}) {
    super(opts.reason || code);
    this.name = "WaylogError";
    this.code = code;
    this.reason = opts.reason;
    this.causeText = opts.cause;
  }
}

export interface Logger {
  info(msg: string, fields?: Fields): void;
  warn(msg: string, fields?: Fields): void;
  error(msg: string, err: WaylogError | undefined, fields?: Fields): void;
}

export interface Context {
  readonly __waylogContext?: unique symbol;
}

export interface BeginOptions {
  traceId?: string;
  spanId?: string;
  parentSpanId?: string;
  now?: Date;
}

export interface ExplainResult {
  traceId: string;
  service: string;
  route: string;
  status: Status;
  anchor?: Anchor;
  path: Step[];
  logs: Log[];
  downstream: Downstream[];
  toString(): string;
}
