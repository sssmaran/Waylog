import type { Signal, WaylogConfig } from "./types.js";
import { normalizeIngestUrl } from "./transport.js";

const maxSignalReasonLen = 512;
const maxSignalMessageLen = 4096;
const signalPostTimeoutMs = 5000;

/**
 * signalUrl resolves the /v1/signals endpoint. signalUrl wins when set;
 * otherwise it derives from ingestUrl, replacing a trailing /v1/events path with
 * /v1/signals and preserving any query parameters.
 */
export function signalUrl(cfg: WaylogConfig): string {
  if (cfg.signalUrl) return cfg.signalUrl;
  if (!cfg.ingestUrl) return "";
  const u = new URL(normalizeIngestUrl(cfg.ingestUrl));
  u.pathname = u.pathname.replace(/\/v1\/events$/, "/v1/signals");
  return u.toString();
}

/**
 * postSignal sends a production signal to the ingest server. It is a no-op when
 * neither signalUrl nor ingestUrl is configured. service, env and timestamp
 * default to config / now when unset, and reason is bounded in length. Honors an
 * injected cfg.fetch. Success = any 2xx.
 *
 * The request is bounded by signalPostTimeoutMs: an observability SDK must never
 * keep a broken process alive waiting on a hung endpoint, and the unhandled-
 * rejection handler defers the process crash until this POST settles.
 */
export async function postSignal(cfg: WaylogConfig, signal: Signal): Promise<void> {
  const url = signalUrl(cfg);
  if (!url) return;
  const fetchImpl = cfg.fetch ?? fetch;
  const body: Signal = {
    ...signal,
    service: signal.service || cfg.service,
    env: signal.env || cfg.env || "",
    timestamp: signal.timestamp || new Date().toISOString(),
    ...(signal.reason ? { reason: truncate(signal.reason, maxSignalReasonLen) } : {}),
    ...(signal.message ? { message: truncate(signal.message, maxSignalMessageLen) } : {}),
  };
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (cfg.apiKey) headers.Authorization = `Bearer ${cfg.apiKey}`;
  const resp = await fetchImpl(url, {
    method: "POST",
    headers,
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(signalPostTimeoutMs),
  });
  if (resp.status < 200 || resp.status >= 300) {
    throw new Error(`waylog signals error ${resp.status}`);
  }
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) : s;
}
