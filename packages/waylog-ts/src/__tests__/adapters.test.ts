import { describe, expect, it, vi } from "vitest";
import { middleware as expressMiddleware, useLogger as useExpressLogger } from "../express.js";
import { middleware as honoMiddleware, useLogger as useHonoLogger } from "../hono.js";
import { middleware as nestMiddleware } from "../nest.js";
import { middleware as nextMiddleware } from "../next.js";
import { stats } from "../index.js";
import type { WideEvent } from "../types.js";

function outputCapture() {
  const lines: string[] = [];
  return {
    config: { service: "web", env: "test", output: (line: string) => lines.push(line) },
    event(): WideEvent {
      const raw = lines.find((line) => line.trim().startsWith("{"));
      if (!raw) throw new Error("no event");
      return JSON.parse(raw) as WideEvent;
    },
  };
}

function mockExpress(headers: Record<string, string> = {}) {
  const listeners: Record<string, Array<() => void>> = {};
  const req = { method: "GET", url: "/checkout/42", route: { path: "/checkout/:id" }, headers };
  const res = {
    statusCode: 200,
    setHeader: vi.fn(),
    on: (ev: "finish" | "close", cb: () => void) => {
      (listeners[ev] ??= []).push(cb);
    },
    trigger: (ev: "finish" | "close") => listeners[ev]?.forEach((cb) => cb()),
  };
  return { req, res };
}

describe("TS adapters", () => {
  it("Express captures route, trace, status, and logger fields", async () => {
    const h = outputCapture();
    const mw = expressMiddleware(h.config);
    const { req, res } = mockExpress({ traceparent: `00-${"a".repeat(32)}-${"b".repeat(16)}-01` });
    mw(req as any, res as any, () => {
      useExpressLogger(req as any).info("hello", { user_id: "u_1" });
    });
    res.trigger("finish");
    const ev = h.event();
    expect(ev.trace_id).toBe("a".repeat(32));
    expect((ev.fields?.http as any).route).toBe("/checkout/:id");
    expect(ev.logs?.[0]?.fields).toEqual({ user_id: "u_1" });
  });

  it("Express marks 5xx as failures", () => {
    const h = outputCapture();
    const mw = expressMiddleware(h.config);
    const { req, res } = mockExpress();
    mw(req as any, res as any, () => undefined);
    res.statusCode = 503;
    res.trigger("finish");
    expect(h.event().anchor?.error_code).toBe("HTTP_503");
  });

  it("Express thrown errors finalize once", () => {
    const h = outputCapture();
    const mw = expressMiddleware(h.config);
    const { req, res } = mockExpress();
    expect(() => mw(req as any, res as any, () => {
      throw new Error("boom");
    })).toThrow("boom");
    res.trigger("finish");
    res.trigger("close");
    expect(h.event().status).toBe("error");
    expect(stats().lateCompletionAfterEmit).toBe(0);
  });

  it("Hono captures thrown errors", async () => {
    const h = outputCapture();
    const store = new Map<string, unknown>();
    const c = {
      req: { method: "POST", routePath: "/buy/:id", path: "/buy/42", header: () => undefined },
      res: { headers: { set: vi.fn() }, status: 200 },
      set: (k: string, v: unknown) => store.set(k, v),
      get: (k: string) => store.get(k),
    };
    const mw = honoMiddleware(h.config);
    await expect(mw(c as any, async () => {
      useHonoLogger(c as any).warn("before boom");
      throw new Error("boom");
    })).rejects.toThrow("boom");
    const ev = h.event();
    expect(ev.status).toBe("error");
    expect((ev.fields?.http as any).status).toBe(500);
  });

  it("Next wrapper finalizes handler output", async () => {
    const h = outputCapture();
    const handler = nextMiddleware(h.config, async (_req, ctx) => {
      return { status: 201, ctx };
    });
    const res = await handler({ method: "GET", url: "http://local/items", headers: { get: () => null } });
    expect(res.status).toBe(201);
    expect((h.event().fields?.http as any).route).toBe("/items");
  });

  it("Nest middleware reuses Express lifecycle semantics", () => {
    const h = outputCapture();
    const mw = nestMiddleware(h.config);
    const { req, res } = mockExpress();
    mw(req as any, res as any, () => undefined);
    res.statusCode = 204;
    res.trigger("finish");
    expect((h.event().fields?.http as any).status).toBe(204);
  });
});
