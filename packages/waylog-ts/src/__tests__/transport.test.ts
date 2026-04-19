import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Transport } from "../transport.js";
import { SCHEMA_VERSION, type WideEvent } from "../types.js";

function sampleEvent(n = 0): WideEvent {
  return {
    schema_version: SCHEMA_VERSION,
    event_name: "svc.request",
    timestamp: new Date(0).toISOString(),
    user: { id: "u", tier: "free", region: "us", vip: false },
    request: { trace_id: "a".repeat(32), span_id: "b".repeat(16), flow: "f", feature_flags: [] },
    system: { service: "svc", version: "1", deployment_id: "", env: "test" },
    outcome: { success: true, status_code: 200 + n, kind: "http" },
    metrics: { latency_ms: n },
  };
}

describe("Transport", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("flushes once batchSize is reached", async () => {
    const fakeFetch = vi.fn(async () => new Response("ok", { status: 200 }));
    const t = new Transport({
      endpoint: "http://x",
      service: "svc",
      batchSize: 2,
      flushIntervalMs: 10_000,
      fetch: fakeFetch as unknown as typeof fetch,
    });
    t.enqueue(sampleEvent(1));
    expect(fakeFetch).not.toHaveBeenCalled();
    t.enqueue(sampleEvent(2));
    // The second enqueue triggers flush synchronously as a microtask.
    await vi.waitFor(() => expect(fakeFetch).toHaveBeenCalledTimes(1));
    const call = fakeFetch.mock.calls[0]!;
    const body = JSON.parse(call[1]!.body as string) as WideEvent[];
    expect(body).toHaveLength(2);
  });

  it("drops events once queueMax is hit and reports count", () => {
    const t = new Transport({
      endpoint: "http://x",
      service: "svc",
      queueMax: 1,
      batchSize: 100,
      flushIntervalMs: 10_000,
      fetch: vi.fn() as unknown as typeof fetch,
    });
    expect(t.enqueue(sampleEvent())).toBe(true);
    expect(t.enqueue(sampleEvent())).toBe(false);
    expect(t.droppedCount()).toBe(1);
  });

  it("counts network failures as drops", async () => {
    const t = new Transport({
      endpoint: "http://x",
      service: "svc",
      batchSize: 1,
      fetch: vi.fn(async () => {
        throw new Error("econnrefused");
      }) as unknown as typeof fetch,
    });
    t.enqueue(sampleEvent());
    await vi.waitFor(() => expect(t.droppedCount()).toBe(1));
  });

  it("sends a single object (not an array) when batch size is 1", async () => {
    const fakeFetch = vi.fn(async () => new Response("ok"));
    const t = new Transport({
      endpoint: "http://x",
      service: "svc",
      batchSize: 1,
      fetch: fakeFetch as unknown as typeof fetch,
    });
    t.enqueue(sampleEvent());
    await vi.waitFor(() => expect(fakeFetch).toHaveBeenCalled());
    const body = JSON.parse(fakeFetch.mock.calls[0]![1]!.body as string);
    expect(Array.isArray(body)).toBe(false);
    expect(body.event_name).toBe("svc.request");
  });

  it("flushes remaining queue on close", async () => {
    const fakeFetch = vi.fn(async () => new Response("ok"));
    const t = new Transport({
      endpoint: "http://x",
      service: "svc",
      batchSize: 100,
      flushIntervalMs: 10_000,
      fetch: fakeFetch as unknown as typeof fetch,
    });
    t.enqueue(sampleEvent());
    await t.close();
    expect(fakeFetch).toHaveBeenCalledTimes(1);
  });
});
