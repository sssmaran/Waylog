import { describe, expect, it, vi } from "vitest";
import { Transport, normalizeIngestUrl } from "../transport.js";
import type { WideEvent } from "../types.js";

function event(id: string, status: WideEvent["status"] = "ok"): WideEvent {
  return {
    schema_version: "2.0",
    event_id: id,
    ts_start: new Date(0).toISOString(),
    ts_end: new Date(1).toISOString(),
    duration_ms: 1,
    kind: "http",
    service: "checkout",
    env: "test",
    trace_id: "a".repeat(32),
    span_id: "b".repeat(16),
    parent_span_id: "",
    status,
  };
}

describe("v2 transport", () => {
  it("normalizes ingest URLs", () => {
    expect(normalizeIngestUrl("http://x")).toBe("http://x/v1/events");
    expect(normalizeIngestUrl("http://x/v1/events")).toBe("http://x/v1/events");
    expect(normalizeIngestUrl("http://x?token=abc")).toBe("http://x/v1/events?token=abc");
  });

  it("supports single-event JSON mode", async () => {
    const calls: RequestInit[] = [];
    const t = new Transport({
      service: "checkout",
      env: "test",
      ingestUrl: "http://x",
      batchMode: false,
      fetch: vi.fn(async (_url, init) => {
        calls.push(init!);
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });
    t.submit(event("json-1"));
    await t.shutdown();
    expect(calls[0]?.headers).toMatchObject({ "Content-Type": "application/json" });
    expect(JSON.parse(String(calls[0]?.body))).toMatchObject({ event_id: "json-1" });
  });

  it("posts NDJSON with auth", async () => {
    const calls: RequestInit[] = [];
    const t = new Transport({
      service: "checkout",
      env: "test",
      ingestUrl: "http://x",
      apiKey: "k",
      fetch: vi.fn(async (_url, init) => {
        calls.push(init!);
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });
    t.submit(event("e1"));
    await t.shutdown();
    expect(calls[0]?.headers).toMatchObject({ "Content-Type": "application/x-ndjson", Authorization: "Bearer k" });
    expect(String(calls[0]?.body)).toContain("\"event_id\":\"e1\"");
  });

  it("counts envelope rejections under 200 responses", async () => {
    const t = new Transport({
      service: "checkout",
      env: "test",
      ingestUrl: "http://x",
      fetch: vi.fn(async () => new Response(
        JSON.stringify({ accepted: 0, duplicate: 0, rejected: [{ index: 0, event_id: "e1", reason: "validation_failed" }] }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as unknown as typeof fetch,
    });
    t.submit(event("e1", "error"));
    await t.shutdown();
    expect(t.rejectedCount()).toBe(1);
  });

  it("counts deprecation headers even when the 2xx body is empty", async () => {
    const t = new Transport({
      service: "checkout",
      env: "test",
      ingestUrl: "http://x",
      fetch: vi.fn(async () => new Response("", { status: 202, headers: { Deprecation: "true" } })) as unknown as typeof fetch,
    });
    t.submit(event("e1", "ok"));
    await t.shutdown();
    expect(t.deprecatedCount()).toBe(1);
  });

  it("retries transient failures", async () => {
    let calls = 0;
    const t = new Transport({
      service: "checkout",
      env: "test",
      ingestUrl: "http://x",
      fetch: vi.fn(async () => {
        calls++;
        return new Response("x", { status: calls === 1 ? 500 : 200 });
      }) as unknown as typeof fetch,
    });
    t.submit(event("e1", "error"));
    await t.shutdown(1000);
    expect(calls).toBeGreaterThanOrEqual(2);
    expect(t.failureCount()).toBe(1);
  });

  it("counts permanent drops", async () => {
    const t = new Transport({
      service: "checkout",
      env: "test",
      ingestUrl: "http://x",
      fetch: vi.fn(async () => new Response("bad", { status: 400 })) as unknown as typeof fetch,
    });
    t.submit(event("bad", "error"));
    await t.shutdown();
    expect(t.droppedCount()).toBe(1);
  });

  it("drops ok queue under pressure before priority", () => {
    const t = new Transport({
      service: "checkout",
      env: "test",
      ingestUrl: "http://x",
      maxInFlightBytes: 500,
      fetch: vi.fn() as unknown as typeof fetch,
    });
    expect(t.submit(event("ok-1"))).toBe(true);
    expect(t.submit(event("ok-2"))).toBe(true);
    expect(t.submit(event("prio", "error"))).toBe(true);
    expect(t.droppedCount()).toBeGreaterThan(0);
  });
});
