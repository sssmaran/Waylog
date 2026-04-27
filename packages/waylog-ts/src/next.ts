import { begin, fail, finalize, finalizeTimeout, from, init, newError, parseTraceparent, runWithContext, setField, setHTTPStatus, type Context } from "./index.js";
import type { Logger, WaylogConfig } from "./types.js";

type NextRequestLike = {
  method: string;
  url: string;
  headers: { get(name: string): string | null };
};
type NextResponseLike = { status?: number };

export type NextHandler<T extends NextResponseLike = NextResponseLike> = (req: NextRequestLike, ctx: Context) => Promise<T> | T;

export function middleware<T extends NextResponseLike = NextResponseLike>(config: WaylogConfig, handler: NextHandler<T>) {
  init(config);
  return async function waylogNext(req: NextRequestLike): Promise<T> {
    const inbound = parseTraceparent(req.headers.get("traceparent"));
    const ctx = begin({}, { traceId: inbound?.traceId, parentSpanId: inbound?.spanId });
    setField(ctx, "http", { method: req.method, route: new URL(req.url).pathname, status: 200 });
    let timer: ReturnType<typeof setTimeout> | undefined;
    if (config.maxRequestAgeMs && config.maxRequestAgeMs > 0) timer = setTimeout(() => void finalizeTimeout(ctx), config.maxRequestAgeMs);
    try {
      const res = await runWithContext(ctx, () => handler(req, ctx));
      setHTTPStatus(ctx, res.status ?? 200);
      if ((res.status ?? 200) >= 500) fail(ctx, newError(`HTTP_${res.status ?? 500}`, { reason: `HTTP ${res.status ?? 500}` }));
      await finalize(ctx);
      return res;
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

export function useLogger(ctx: Context): Logger {
  return from(ctx);
}
