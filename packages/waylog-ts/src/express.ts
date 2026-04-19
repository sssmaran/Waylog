import { createLogger, startRequest, type Logger } from "./logger.js";
import type { RequestLogger, WaylogConfig } from "./types.js";

// Express-shaped types without taking a runtime dependency on express.
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

const LOGGER_KEY = Symbol.for("waylog.request.logger");

export interface ExpressMiddlewareOptions {
  logger?: Logger;
}

// waylog() creates (or reuses) a Logger, attaches a per-request RequestLogger
// to req[Symbol.for("waylog.request.logger")], and emits on response finish.
export function waylog(config: WaylogConfig & ExpressMiddlewareOptions) {
  const logger = config.logger ?? createLogger(config);

  return function middleware(req: Req, res: Res, next: Next): void {
    const { log, traceparent } = startRequest(logger, {
      method: req.method,
      route: req.route?.path ?? req.url,
      traceparentHeader: pickHeader(req.headers["traceparent"]),
    });
    (req as any)[LOGGER_KEY] = log;
    res.setHeader?.("traceparent", traceparent);

    const finalize = (): void => {
      if (log.emitted()) return;
      log.emit({ success: res.statusCode < 500, status_code: res.statusCode });
    };
    res.on("finish", finalize);
    res.on("close", finalize);
    next();
  };
}

export function useLogger(req: Req): RequestLogger {
  const l = (req as any)[LOGGER_KEY] as RequestLogger | undefined;
  if (!l) throw new Error("waylog: useLogger() called before waylog() middleware was registered");
  return l;
}

function pickHeader(v: string | string[] | undefined): string | undefined {
  if (Array.isArray(v)) return v[0];
  return v;
}
