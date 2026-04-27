import {
  begin,
  fail,
  finalize,
  finalizeAborted,
  finalizeTimeout,
  formatTraceparent,
  from,
  init,
  newError,
  parseTraceparent,
  runWithContext,
  setField,
  setHTTPStatus,
  spanId,
  traceId,
  type Context,
} from "./index.js";
import type { Logger, WaylogConfig } from "./types.js";

type Req = {
  method?: string;
  route?: { path?: string };
  url?: string;
  headers: Record<string, string | string[] | undefined>;
  [key: string]: unknown;
};
type Res = {
  statusCode: number;
  setHeader?: (name: string, value: string) => void;
  on(event: "finish" | "close", cb: () => void): void;
};
type Next = (err?: unknown) => void;

const CTX_KEY = Symbol.for("waylog.v2.express.context");

export function middleware(config: WaylogConfig) {
  init(config);
  return function waylogExpress(req: Req, res: Res, next: Next): void {
    const inbound = parseTraceparent(pickHeader(req.headers.traceparent));
    const ctx = begin(req as Context, { traceId: inbound?.traceId, parentSpanId: inbound?.spanId });
    setField(ctx, "http", { method: req.method ?? "", route: req.route?.path ?? req.url ?? "", status: 200 });
    (req as Record<symbol, Context>)[CTX_KEY] = ctx;
    res.setHeader?.("traceparent", formatTraceparent(traceId(ctx), spanId(ctx)));

    let finalized = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const markFinalized = (): boolean => {
      if (finalized) return false;
      finalized = true;
      if (timer) clearTimeout(timer);
      return true;
    };
    const finish = (): void => {
      if (!markFinalized()) return;
      setHTTPStatus(ctx, res.statusCode || 200);
      if (res.statusCode >= 500) fail(ctx, newError(`HTTP_${res.statusCode}`, { reason: `HTTP ${res.statusCode}` }));
      void finalize(ctx);
    };
    const close = (): void => {
      if (!markFinalized()) return;
      void finalizeAborted(ctx);
    };
    res.on("finish", finish);
    res.on("close", close);
    if (config.maxRequestAgeMs && config.maxRequestAgeMs > 0) {
      timer = setTimeout(() => {
        if (!markFinalized()) return;
        void finalizeTimeout(ctx);
      }, config.maxRequestAgeMs);
    }
    try {
      runWithContext(ctx, () => next());
    } catch (err) {
      markFinalized();
      setHTTPStatus(ctx, 500);
      fail(ctx, newError("ERR", { reason: err instanceof Error ? err.message : String(err) }));
      void finalize(ctx);
      throw err;
    }
  };
}

export const waylog = middleware;

export function useLogger(req: Req): Logger {
  const ctx = (req as Record<symbol, Context | undefined>)[CTX_KEY];
  if (!ctx) throw new Error("waylog: useLogger() called before middleware was registered");
  return from(ctx);
}

function pickHeader(v: string | string[] | undefined): string | undefined {
  return Array.isArray(v) ? v[0] : v;
}
