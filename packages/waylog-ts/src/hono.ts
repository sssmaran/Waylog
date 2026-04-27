import {
  begin,
  fail,
  finalize,
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

type Ctx = {
  req: { method: string; routePath?: string; path?: string; header: (n: string) => string | undefined };
  res: { headers: { set: (n: string, v: string) => void }; status?: number };
  set: (key: string, value: unknown) => void;
  get: (key: string) => unknown;
};

const CTX_KEY = "__waylog_v2_context__";

export function middleware(config: WaylogConfig) {
  init(config);
  return async function waylogHono(c: Ctx, next: () => Promise<void>): Promise<void> {
    const inbound = parseTraceparent(c.req.header("traceparent"));
    const ctx = begin({}, { traceId: inbound?.traceId, parentSpanId: inbound?.spanId });
    setField(ctx, "http", { method: c.req.method, route: c.req.routePath ?? c.req.path ?? "", status: 200 });
    c.set(CTX_KEY, ctx);
    c.res.headers.set("traceparent", formatTraceparent(traceId(ctx), spanId(ctx)));

    let timer: ReturnType<typeof setTimeout> | undefined;
    if (config.maxRequestAgeMs && config.maxRequestAgeMs > 0) {
      timer = setTimeout(() => void finalizeTimeout(ctx), config.maxRequestAgeMs);
    }
    try {
      await runWithContext(ctx, () => next());
      setHTTPStatus(ctx, c.res.status ?? 200);
      if ((c.res.status ?? 200) >= 500) fail(ctx, newError(`HTTP_${c.res.status ?? 500}`, { reason: `HTTP ${c.res.status ?? 500}` }));
      await finalize(ctx);
    } catch (err) {
      setHTTPStatus(ctx, 500);
      fail(ctx, newError("ERR", { reason: err instanceof Error ? err.message : String(err) }));
      await finalize(ctx);
      throw err;
    } finally {
      if (timer) clearTimeout(timer);
    }
  };
}

export const waylog = middleware;

export function useLogger(c: Ctx): Logger {
  const ctx = c.get(CTX_KEY) as Context | undefined;
  if (!ctx) throw new Error("waylog: useLogger() called before middleware was registered");
  return from(ctx);
}
