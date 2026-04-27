import { AsyncLocalStorage } from "node:async_hooks";
import { randomBytes, randomUUID } from "node:crypto";
import { isReservedCode, isWaylogError, newError as buildError } from "./error.js";
import { Transport } from "./transport.js";
import {
  SCHEMA_VERSION,
  WaylogError,
  type Anchor,
  type BeginOptions,
  type Context,
  type Downstream,
  type ErrorOpts,
  type ErrorRef,
  type ExplainResult,
  type Fields,
  type Log,
  type LogLevel,
  type Logger,
  type Stats,
  type Status,
  type Step,
  type StepError,
  type WaylogConfig,
  type WideEvent,
} from "./types.js";

const defaultMaxSteps = 128;
const defaultMaxLogs = 256;
const defaultMaxBufferBytes = 512 * 1024;
const ctxKey = Symbol.for("waylog.v2.request");
const als = new AsyncLocalStorage<RequestState>();

type ActiveStep = {
  name: string;
  startedAt: number;
  startMs: number;
  spanId?: string;
  downstream?: Downstream;
};

type RequestState = {
  sdk: SDK;
  eventId: string;
  tsStart: Date;
  traceId: string;
  spanId: string;
  parentSpanId: string;
  fields: Fields;
  steps: Step[];
  logs: Log[];
  errors: ErrorRef[];
  activeSteps: ActiveStep[];
  sealed: boolean;
  suppressed: boolean;
  headerOnly: boolean;
  finalStatus?: Status;
  anchor?: Anchor;
  bufBytes: number;
};

class SDK {
  readonly cfg: RequiredConfig;
  readonly transport?: Transport;
  readonly active = new Set<RequestState>();
  readonly devEnabled: boolean;
  rateSecond = 0;
  rateCount = 0;
  stats: Stats = {
    activeRequests: 0,
    eventsEmitted: 0,
    eventsSuppressed: 0,
    stepsDropped: 0,
    logsDropped: 0,
    bytesDroppedFromBuffer: 0,
    bufferOverflows: 0,
    reservedCodeRejections: 0,
    suppressedThenFailed: 0,
    lateCompletionAfterEmit: 0,
    eventsDropped: 0,
    deliveryFailures: 0,
  };

  constructor(cfg: RequiredConfig) {
    this.cfg = cfg;
    this.devEnabled = cfg.devMode || cfg.env.toLowerCase() === "dev";
    if (cfg.ingestUrl) this.transport = new Transport(cfg);
  }
}

type RequiredConfig = WaylogConfig & {
  output: NodeJS.WritableStream | ((line: string) => void);
  maxSteps: number;
  maxLogs: number;
  maxBufferBytes: number;
  maxInFlightBytes: number;
  maxEventsPerSec: number;
};

let sdk: SDK | undefined;

export function init(cfg: WaylogConfig): void {
  if (!cfg.service) throw new Error("waylog: service is required");
  if (!cfg.env) throw new Error("waylog: env is required");
  sdk = new SDK({
    ...cfg,
    output: cfg.output ?? process.stderr,
    maxSteps: cfg.maxSteps && cfg.maxSteps > 0 ? cfg.maxSteps : defaultMaxSteps,
    maxLogs: cfg.maxLogs && cfg.maxLogs > 0 ? cfg.maxLogs : defaultMaxLogs,
    maxBufferBytes: cfg.maxBufferBytes && cfg.maxBufferBytes > 0 ? cfg.maxBufferBytes : defaultMaxBufferBytes,
    maxInFlightBytes: cfg.maxInFlightBytes && cfg.maxInFlightBytes > 0 ? cfg.maxInFlightBytes : 10 << 20,
    maxEventsPerSec: cfg.maxEventsPerSec ?? 0,
  });
}

export async function shutdown(timeoutMs = 0): Promise<void> {
  if (!sdk) return;
  await sdk.transport?.shutdown(timeoutMs);
}

export function stats(): Stats {
  if (!sdk) return emptyStats();
  return {
    ...sdk.stats,
    activeRequests: sdk.active.size,
    eventsDropped: sdk.stats.eventsDropped + (sdk.transport?.droppedCount() ?? 0),
    deliveryFailures: sdk.stats.deliveryFailures + (sdk.transport?.failureCount() ?? 0),
  };
}

export function begin(ctx: Context = {}, opts: BeginOptions = {}): Context {
  const s = ensureSDK();
  const now = opts.now ?? new Date();
  const r: RequestState = {
    sdk: s,
    eventId: randomUUID(),
    tsStart: now,
    traceId: opts.traceId || newTraceId(),
    spanId: opts.spanId || newSpanId(),
    parentSpanId: opts.parentSpanId ?? "",
    fields: {},
    steps: [],
    logs: [],
    errors: [],
    activeSteps: [],
    sealed: false,
    suppressed: false,
    headerOnly: false,
    bufBytes: 0,
  };
  s.active.add(r);
  return attach(ctx, r);
}

export function runWithContext<T>(ctx: Context, fn: () => T): T {
  const r = requestFrom(ctx);
  if (!r) return fn();
  return als.run(r, fn);
}

export function from(ctx?: Context): Logger {
  const r = ctx ? requestFrom(ctx) : als.getStore();
  return {
    info: (msg, fields) => log(r, "info", msg, fields),
    warn: (msg, fields) => log(r, "warn", msg, fields),
    error: (msg, err, fields) => {
      failState(r, err);
      log(r, "error", msg, fields);
    },
  };
}

export async function step<T>(ctx: Context, name: string, fn: (ctx: Context) => Promise<T>): Promise<T> {
  const r = requestFrom(ctx);
  if (!r || !name) return fn(ctx);
  pushStep(r, name);
  try {
    const v = await als.run(r, () => fn(ctx));
    closeStep(r, name);
    return v;
  } catch (err) {
    closeStep(r, name, err);
    throw err;
  }
}

export function stepSync<T>(ctx: Context, name: string, fn: (ctx: Context) => T): T {
  const r = requestFrom(ctx);
  if (!r || !name) return fn(ctx);
  pushStep(r, name);
  try {
    const v = als.run(r, () => fn(ctx));
    closeStep(r, name);
    return v;
  } catch (err) {
    closeStep(r, name, err);
    throw err;
  }
}

export function fail(ctx: Context, err: WaylogError): void {
  failState(requestFrom(ctx), err);
}

export function newError(code: string, opts: ErrorOpts = {}): WaylogError {
  try {
    return buildError(code, opts);
  } catch (err) {
    if (isReservedCode(code) && sdk) sdk.stats.reservedCodeRejections++;
    throw err;
  }
}

export function suppress(ctx: Context): void {
  const r = requestFrom(ctx);
  if (!r || r.sealed || r.suppressed) return;
  r.suppressed = true;
  r.steps = [];
  r.logs = [];
  r.errors = [];
  r.activeSteps = [];
  r.anchor = undefined;
  r.finalStatus = undefined;
  r.headerOnly = false;
  r.bufBytes = 0;
}

export function setField(ctx: Context, key: string, value: unknown): void {
  const r = requestFrom(ctx);
  if (!r || !key || r.sealed || r.suppressed) return;
  r.fields[key] = cloneDeep(value);
}

export function setHTTPStatus(ctx: Context, status: number): void {
  const r = requestFrom(ctx);
  if (!r || r.sealed || r.suppressed) return;
  const http = ensureHTTPFields(r);
  http.status = status;
}

export function setHTTPRoute(ctx: Context, route: string): void {
  const r = requestFrom(ctx);
  if (!r || !route || r.sealed || r.suppressed) return;
  const http = ensureHTTPFields(r);
  http.route = route;
}

export function recordOutgoingSpan(ctx: Context, spanId: string, service: string, endpoint: string): void {
  const r = requestFrom(ctx);
  if (!r || r.sealed || r.suppressed || !spanId || r.activeSteps.length === 0) return;
  const top = r.activeSteps[r.activeSteps.length - 1]!;
  top.spanId = spanId;
  top.downstream = { service, endpoint, kind: "rpc" };
}

export async function finalize(ctx: Context): Promise<WideEvent | undefined> {
  return finalizeWith(ctx, "normal");
}

export async function finalizePanic(ctx: Context): Promise<WideEvent | undefined> {
  return finalizeWith(ctx, "panic");
}

export async function finalizeAborted(ctx: Context): Promise<WideEvent | undefined> {
  return finalizeWith(ctx, "aborted");
}

export async function finalizeTimeout(ctx: Context): Promise<WideEvent | undefined> {
  return finalizeWith(ctx, "timeout");
}

export async function explain(ctx: Context): Promise<ExplainResult> {
  const r = requestFrom(ctx);
  if (!r) throw new Error("waylog: no active request");
  const status = statusOf(r);
  const http = (r.fields.http ?? {}) as Fields;
  const downstream = r.steps.flatMap((s) => (s.downstream ? [s.downstream] : []));
  const trace = r.traceId;
  const service = r.sdk.cfg.service;
  const anchor = r.anchor;
  return {
    traceId: trace,
    service,
    route: String(http.route ?? ""),
    status,
    anchor,
    path: [...r.steps],
    logs: [...r.logs],
    downstream,
    toString() {
      const anchorText = anchor ? `${anchor.step} ${anchor.error_code}` : "none";
      return `trace ${trace} service=${service} status=${status} anchor=${anchorText}`;
    },
  };
}

export function newTraceId(): string {
  return randomBytes(16).toString("hex");
}

export function newSpanId(): string {
  return randomBytes(8).toString("hex");
}

export function parseTraceparent(h: string | undefined | null): { traceId: string; spanId: string } | undefined {
  if (!h) return undefined;
  const parts = h.trim().split("-");
  if (parts.length !== 4) return undefined;
  const [version, traceId, spanId] = parts;
  if (version !== "00") return undefined;
  if (!/^[0-9a-f]{32}$/.test(traceId ?? "") || !/^[0-9a-f]{16}$/.test(spanId ?? "")) return undefined;
  return { traceId: traceId!, spanId: spanId! };
}

export function formatTraceparent(traceId: string, spanId: string): string {
  return `00-${traceId}-${spanId}-01`;
}

export function traceId(ctx: Context): string {
  return requestFrom(ctx)?.traceId ?? "";
}

export function spanId(ctx: Context): string {
  return requestFrom(ctx)?.spanId ?? "";
}

function finalizeWith(ctx: Context, lifecycle: "normal" | "panic" | "aborted" | "timeout"): WideEvent | undefined {
  const r = requestFrom(ctx);
  if (!r) throw new Error("waylog: no active request");
  if (r.sealed) {
    r.sdk.stats.lateCompletionAfterEmit++;
    return undefined;
  }
  applyLifecycle(r, lifecycle);
  r.sealed = true;
  const ev = assemble(r, new Date());
  r.sdk.active.delete(r);
  deliver(r.sdk, ev);
  emitDevFinal(r.sdk, ev);
  return ev;
}

function applyLifecycle(r: RequestState, lifecycle: "normal" | "panic" | "aborted" | "timeout"): void {
  if (r.suppressed) return;
  const now = Date.now();
  if (lifecycle === "panic") {
    markLifecycle(r, "error", "WAYLOG_PANIC");
    recordError(r, "WAYLOG_PANIC", "runtime panic recovered");
    flushActiveSteps(r, now, "error", { code: "WAYLOG_PANIC", reason: "runtime panic recovered" });
  } else if (lifecycle === "timeout") {
    markLifecycle(r, "timeout", "WAYLOG_TIMEOUT");
    flushActiveSteps(r, now, "ok");
  } else if (lifecycle === "aborted") {
    if (!r.anchor) markLifecycle(r, "aborted", "WAYLOG_ABORTED");
    flushActiveSteps(r, now, "ok");
  }
}

function assemble(r: RequestState, tsEnd: Date): WideEvent {
  const status = statusOf(r);
  const fields = Object.keys(r.fields).length > 0 ? (r.sdk.cfg.redactor ? r.sdk.cfg.redactor(r.fields) : r.fields) : undefined;
  const ev: WideEvent = {
    schema_version: SCHEMA_VERSION,
    event_id: r.eventId,
    ts_start: r.tsStart.toISOString(),
    ts_end: tsEnd.toISOString(),
    duration_ms: Math.max(0, tsEnd.getTime() - r.tsStart.getTime()),
    kind: "http",
    service: r.sdk.cfg.service,
    env: r.sdk.cfg.env,
    ...(r.sdk.cfg.version ? { version: r.sdk.cfg.version } : {}),
    trace_id: r.traceId,
    span_id: r.spanId,
    parent_span_id: r.parentSpanId,
    status,
    ...(fields ? { fields } : {}),
  };
  if (status === "suppressed") return ev;
  if (r.anchor) ev.anchor = r.anchor;
  if (!r.headerOnly) {
    if (r.steps.length > 0) ev.steps = r.steps;
    if (r.logs.length > 0) ev.logs = r.logs;
    if (r.errors.length > 0) ev.errors = r.errors;
  }
  return ev;
}

function deliver(s: SDK, ev: WideEvent): void {
  if (!allowEvent(s, ev)) {
    s.stats.eventsDropped++;
    return;
  }
  if (s.transport) {
    if (s.transport.submit(ev)) {
      s.stats.eventsEmitted++;
      if (ev.status === "suppressed") s.stats.eventsSuppressed++;
    }
    return;
  }
  writeOutput(s.cfg.output, `${JSON.stringify(ev)}\n`);
  s.stats.eventsEmitted++;
  if (ev.status === "suppressed") s.stats.eventsSuppressed++;
}

function allowEvent(s: SDK, ev: WideEvent): boolean {
  if (s.cfg.maxEventsPerSec <= 0 || ev.status === "error" || ev.status === "timeout" || ev.status === "partial" || ev.status === "aborted") return true;
  const now = Math.floor(Date.now() / 1000);
  if (s.rateSecond !== now) {
    s.rateSecond = now;
    s.rateCount = 0;
  }
  if (s.rateCount >= s.cfg.maxEventsPerSec) return false;
  s.rateCount++;
  return true;
}

function log(r: RequestState | undefined, level: LogLevel, msg: string, fields?: Fields): void {
  if (!r || r.sealed || r.suppressed || r.headerOnly) return;
  const entry: Log = { ts_offset_ms: Date.now() - r.tsStart.getTime(), level, msg, ...(fields ? { fields: cloneTop(fields) } : {}) };
  const bytes = logBytes(entry);
  if (r.logs.length >= r.sdk.cfg.maxLogs || r.bufBytes + bytes > r.sdk.cfg.maxBufferBytes) {
    r.sdk.stats.logsDropped++;
    r.sdk.stats.bytesDroppedFromBuffer += bytes;
    return;
  }
  r.logs.push(entry);
  r.bufBytes += bytes;
  emitDevLog(r.sdk, level, msg, fields);
}

function pushStep(r: RequestState, name: string): void {
  const now = Date.now();
  r.activeSteps.push({ name, startedAt: now, startMs: now - r.tsStart.getTime() });
}

function closeStep(r: RequestState, name: string, err?: unknown): void {
  const active = popStep(r, name);
  if (r.sealed || r.suppressed) return;
  const stepErr = errorFromUnknown(err);
  const step: Step = {
    name,
    ...(active.spanId ? { span_id: active.spanId } : {}),
    start_ms: active.startMs,
    duration_ms: Math.max(0, Date.now() - active.startedAt),
    status: stepErr ? "error" : "ok",
    ...(active.downstream ? { downstream: active.downstream } : {}),
    ...(stepErr ? { error: stepErr } : {}),
  };
  if (stepErr) {
    recordError(r, stepErr.code || "ERR", stepErr.reason);
    if (!r.anchor) r.anchor = { step: name, error_code: stepErr.code || "ERR" };
  }
  addStep(r, step);
}

function addStep(r: RequestState, step: Step): void {
  if (r.headerOnly) {
    r.sdk.stats.stepsDropped++;
    return;
  }
  const bytes = stepBytes(step);
  if (r.steps.length >= r.sdk.cfg.maxSteps || r.bufBytes + bytes > r.sdk.cfg.maxBufferBytes) {
    r.sdk.stats.stepsDropped++;
    r.sdk.stats.bytesDroppedFromBuffer += bytes;
    return;
  }
  r.steps.push(step);
  r.bufBytes += bytes;
}

function flushActiveSteps(r: RequestState, now: number, status: "ok" | "error", err?: StepError): void {
  for (const active of r.activeSteps) {
    addStep(r, {
      name: active.name,
      ...(active.spanId ? { span_id: active.spanId } : {}),
      start_ms: active.startMs,
      duration_ms: Math.max(0, now - active.startedAt),
      status,
      ...(active.downstream ? { downstream: active.downstream } : {}),
      ...(err ? { error: err } : {}),
    });
  }
  r.activeSteps = [];
}

function failState(r: RequestState | undefined, err: WaylogError): void {
  if (!r || !err || r.sealed) return;
  if (r.suppressed) {
    r.sdk.stats.suppressedThenFailed++;
    return;
  }
  if (isReservedCode(err.code)) {
    r.sdk.stats.reservedCodeRejections++;
    return;
  }
  recordError(r, err.code, err.reason);
  if (!r.anchor) r.anchor = { step: r.activeSteps.at(-1)?.name ?? "request", error_code: err.code };
}

function markLifecycle(r: RequestState, status: Status, code: string): void {
  r.finalStatus = status;
  r.anchor = { step: r.activeSteps.at(-1)?.name ?? "request", error_code: code };
}

function statusOf(r: RequestState): Status {
  if (r.suppressed) return "suppressed";
  if (r.finalStatus) return r.finalStatus;
  if (r.anchor) return "error";
  return "ok";
}

function recordError(r: RequestState, code: string, reason?: string): void {
  if (!code || r.errors.some((e) => e.code === code)) return;
  r.errors.push({ code, ...(reason ? { reason } : {}) });
}

function errorFromUnknown(err: unknown): StepError | undefined {
  if (err == null) return undefined;
  if (isWaylogError(err)) {
    if (isReservedCode(err.code) && sdk) {
      sdk.stats.reservedCodeRejections++;
      return { code: "ERR", reason: err.message };
    }
    return { code: err.code, ...(err.reason ? { reason: err.reason } : {}) };
  }
  return { code: "ERR", reason: err instanceof Error ? err.message : String(err) };
}

function popStep(r: RequestState, name: string): ActiveStep {
  let i = -1;
  for (let n = r.activeSteps.length - 1; n >= 0; n--) {
    if (r.activeSteps[n]?.name === name) {
      i = n;
      break;
    }
  }
  if (i < 0) return { name, startedAt: Date.now(), startMs: 0 };
  const [active] = r.activeSteps.splice(i, 1);
  return active!;
}

function attach(ctx: Context, r: RequestState): Context {
  Object.defineProperty(ctx, ctxKey, { value: r, enumerable: false, configurable: true });
  return ctx;
}

function requestFrom(ctx: Context | undefined): RequestState | undefined {
  if (!ctx) return als.getStore();
  return (ctx as Record<symbol, RequestState | undefined>)[ctxKey] ?? als.getStore();
}

function ensureSDK(): SDK {
  if (!sdk) throw new Error("waylog: init() must be called first");
  return sdk;
}

function ensureHTTPFields(r: RequestState): Fields {
  const existing = r.fields.http;
  if (existing && typeof existing === "object" && !Array.isArray(existing)) return existing as Fields;
  const next: Fields = {};
  r.fields.http = next;
  return next;
}

function writeOutput(output: NodeJS.WritableStream | ((line: string) => void), line: string): void {
  if (typeof output === "function") output(line);
  else output.write(line);
}

function emitDevLog(s: SDK, level: LogLevel, msg: string, fields?: Fields): void {
  if (!s.devEnabled) return;
  writeOutput(s.cfg.output, `[${level.toUpperCase()}] ${msg}${fields ? ` ${JSON.stringify(fields)}` : ""}\n`);
}

function emitDevFinal(s: SDK, ev: WideEvent): void {
  if (!s.devEnabled) return;
  writeOutput(s.cfg.output, `${JSON.stringify(ev, null, 2)}\n`);
}

function cloneTop(fields: Fields): Fields {
  return { ...fields };
}

function cloneDeep(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(cloneDeep);
  if (v && typeof v === "object") {
    const out: Fields = {};
    for (const [k, val] of Object.entries(v)) out[k] = cloneDeep(val);
    return out;
  }
  return v;
}

function stepBytes(step: Step): number {
  return JSON.stringify(step).length;
}

function logBytes(entry: Log): number {
  return JSON.stringify(entry).length;
}

function emptyStats(): Stats {
  return {
    activeRequests: 0,
    eventsEmitted: 0,
    eventsSuppressed: 0,
    stepsDropped: 0,
    logsDropped: 0,
    bytesDroppedFromBuffer: 0,
    bufferOverflows: 0,
    reservedCodeRejections: 0,
    suppressedThenFailed: 0,
    lateCompletionAfterEmit: 0,
    eventsDropped: 0,
    deliveryFailures: 0,
  };
}
