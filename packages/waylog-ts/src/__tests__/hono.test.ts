import { describe, expect, it, vi } from "vitest";
import { useLogger, waylog } from "../hono.js";
import type { WideEvent } from "../types.js";

function mockCtx(headers: Record<string, string> = {}) {
  const store = new Map<string, unknown>();
  const resHeaders = new Map<string, string>();
  const c = {
    req: {
      method: "POST",
      routePath: "/checkout",
      path: "/checkout",
      header: (n: string) => headers[n.toLowerCase()],
    },
    res: {
      headers: { set: (n: string, v: string) => resHeaders.set(n, v) },
      status: 200,
    },
    set: (k: string, v: unknown) => store.set(k, v),
    get: (k: string) => store.get(k),
  };
  return { c, store, resHeaders };
}

describe("hono middleware", () => {
  it("attaches logger, surfaces traceparent, and emits after next()", async () => {
    const captured: WideEvent[] = [];
    const fakeFetch = vi.fn(async (_u: string, init?: RequestInit) => {
      captured.push(JSON.parse(init?.body as string));
      return new Response("ok");
    }) as unknown as typeof fetch;

    const mw = waylog({ endpoint: "http://x", service: "web", batchSize: 1, fetch: fakeFetch });
    const { c, resHeaders } = mockCtx({ traceparent: `00-${"a".repeat(32)}-${"b".repeat(16)}-01` });

    await mw(c as any, async () => {
      useLogger(c as any).set({ request: { flow: "checkout" } });
      (c.res as any).status = 201;
    });

    expect(resHeaders.get("traceparent")).toMatch(/^00-a{32}-[0-9a-f]{16}-01$/);
    await vi.waitFor(() => expect(captured).toHaveLength(1));
    expect(captured[0]!.request.trace_id).toBe("a".repeat(32));
    expect(captured[0]!.outcome.status_code).toBe(201);
    expect(captured[0]!.outcome.success).toBe(true);
  });

  it("marks thrown errors as failures and rethrows", async () => {
    const captured: WideEvent[] = [];
    const fakeFetch = vi.fn(async (_u: string, init?: RequestInit) => {
      captured.push(JSON.parse(init?.body as string));
      return new Response("ok");
    }) as unknown as typeof fetch;

    const mw = waylog({ endpoint: "http://x", service: "web", batchSize: 1, fetch: fakeFetch });
    const { c } = mockCtx();

    await expect(
      mw(c as any, async () => {
        throw new Error("boom");
      }),
    ).rejects.toThrow("boom");

    await vi.waitFor(() => expect(captured).toHaveLength(1));
    expect(captured[0]!.outcome.success).toBe(false);
    expect(captured[0]!.outcome.status_code).toBe(500);
    expect(captured[0]!.error?.message).toBe("boom");
  });

  it("useLogger throws when middleware was not registered", () => {
    const { c } = mockCtx();
    expect(() => useLogger(c as any)).toThrow(/middleware/);
  });
});
