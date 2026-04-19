import { createLogger, startRequest, type Logger } from "./logger.js";
import type { RequestLogger, WaylogConfig } from "./types.js";

// Hono-shaped context without runtime dependency on hono.
type Ctx = {
  req: { method: string; routePath?: string; path?: string; header: (n: string) => string | undefined };
  res: { headers: { set: (n: string, v: string) => void } };
  set: (key: string, value: unknown) => void;
  get: (key: string) => unknown;
};

const LOGGER_KEY = "__waylog_request_logger__";

export interface HonoMiddlewareOptions {
  logger?: Logger;
}

// Hono-style middleware: returns an `async (c, next) => { ... }` function.
export function waylog(config: WaylogConfig & HonoMiddlewareOptions) {
  const logger = config.logger ?? createLogger(config);

  return async function middleware(c: Ctx, next: () => Promise<void>): Promise<void> {
    const { log, traceparent } = startRequest(logger, {
      method: c.req.method,
      route: c.req.routePath ?? c.req.path,
      traceparentHeader: c.req.header("traceparent"),
    });
    c.set(LOGGER_KEY, log);
    c.res.headers.set("traceparent", traceparent);

    let statusCode = 200;
    try {
      await next();
      // Hono surfaces final status through c.res, which middleware can read
      // only after next() resolves. We peek defensively via any.
      const anyRes = c.res as unknown as { status?: number };
      if (typeof anyRes.status === "number") statusCode = anyRes.status;
    } catch (err) {
      statusCode = 500;
      log.error(err instanceof Error ? err : String(err));
      throw err;
    } finally {
      if (!log.emitted()) {
        log.emit({ success: statusCode < 500, status_code: statusCode });
      }
    }
  };
}

export function useLogger(c: Ctx): RequestLogger {
  const l = c.get(LOGGER_KEY) as RequestLogger | undefined;
  if (!l) throw new Error("waylog: useLogger() called before waylog() middleware was registered");
  return l;
}
