import { describe, expect, it, vi } from "vitest";
import { useLogger, waylog } from "../express.js";
import type { WideEvent } from "../types.js";

// Synthetic Express-compatible req/res just rich enough to exercise the
// middleware. No actual express dependency.
function mockReqRes(headers: Record<string, string> = {}) {
  const listeners: Record<string, Array<() => void>> = {};
  const res = {
    statusCode: 200,
    setHeader: vi.fn(),
    on: (ev: string, cb: () => void) => {
      (listeners[ev] ??= []).push(cb);
    },
    trigger: (ev: string) => (listeners[ev] || []).forEach((cb) => cb()),
  };
  const req = {
    method: "GET",
    url: "/checkout",
    route: { path: "/checkout" },
    headers,
  };
  return { req, res };
}

describe("express middleware", () => {
  it("attaches a RequestLogger, emits on response finish, and sets traceparent", async () => {
    const captured: WideEvent[] = [];
    const fakeFetch = vi.fn(async (_u: string, init?: RequestInit) => {
      const parsed = JSON.parse(init?.body as string);
      if (Array.isArray(parsed)) captured.push(...parsed);
      else captured.push(parsed);
      return new Response("ok");
    }) as unknown as typeof fetch;

    const mw = waylog({ endpoint: "http://x", service: "web", batchSize: 1, fetch: fakeFetch });
    const { req, res } = mockReqRes({ traceparent: `00-${"a".repeat(32)}-${"b".repeat(16)}-01` });
    const next = vi.fn();
    mw(req as any, res as any, next);
    expect(next).toHaveBeenCalled();
    expect(res.setHeader).toHaveBeenCalledWith("traceparent", expect.stringMatching(/^00-a{32}-[0-9a-f]{16}-01$/));

    const log = useLogger(req as any);
    log.set({ user: { id: "u9", tier: "pro", region: "us", vip: false } });
    res.statusCode = 200;
    res.trigger("finish");
    await vi.waitFor(() => expect(captured).toHaveLength(1));
    expect(captured[0]!.request.trace_id).toBe("a".repeat(32));
    expect(captured[0]!.user.id).toBe("u9");
    expect(captured[0]!.outcome.status_code).toBe(200);
    expect(captured[0]!.outcome.success).toBe(true);
  });

  it("marks 5xx as failures", async () => {
    const captured: WideEvent[] = [];
    const fakeFetch = vi.fn(async (_u: string, init?: RequestInit) => {
      captured.push(JSON.parse(init?.body as string));
      return new Response("ok");
    }) as unknown as typeof fetch;
    const mw = waylog({ endpoint: "http://x", service: "web", batchSize: 1, fetch: fakeFetch });
    const { req, res } = mockReqRes();
    mw(req as any, res as any, () => {});
    res.statusCode = 503;
    res.trigger("finish");
    await vi.waitFor(() => expect(captured).toHaveLength(1));
    expect(captured[0]!.outcome.success).toBe(false);
    expect(captured[0]!.outcome.status_code).toBe(503);
  });

  it("useLogger throws when middleware was not registered", () => {
    const req = { headers: {} } as any;
    expect(() => useLogger(req)).toThrow(/middleware/);
  });
});
