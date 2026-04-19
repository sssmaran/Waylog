import { describe, expect, it, vi } from "vitest";
import { createLogger, newSpanId, newTraceId, parseTraceparent } from "../logger.js";
import type { WideEvent } from "../types.js";

function captureLogger() {
  const captured: WideEvent[] = [];
  const fakeFetch = vi.fn(async (_url: string, init?: RequestInit) => {
    const body = init?.body as string;
    const parsed = JSON.parse(body);
    if (Array.isArray(parsed)) captured.push(...(parsed as WideEvent[]));
    else captured.push(parsed as WideEvent);
    return new Response("ok");
  }) as unknown as typeof fetch;
  const logger = createLogger({
    endpoint: "http://test",
    service: "svc",
    version: "1.0.0",
    env: "test",
    batchSize: 1,
    fetch: fakeFetch,
  });
  return { logger, captured };
}

describe("trace IDs", () => {
  it("newTraceId returns 32 hex chars", () => {
    expect(newTraceId()).toMatch(/^[0-9a-f]{32}$/);
  });
  it("newSpanId returns 16 hex chars", () => {
    expect(newSpanId()).toMatch(/^[0-9a-f]{16}$/);
  });
});

describe("parseTraceparent", () => {
  it("parses a well-formed header", () => {
    const h = `00-${"a".repeat(32)}-${"b".repeat(16)}-01`;
    expect(parseTraceparent(h)).toEqual({ traceId: "a".repeat(32), spanId: "b".repeat(16) });
  });
  it("rejects wrong version, lengths, and non-hex", () => {
    expect(parseTraceparent(undefined)).toBeUndefined();
    expect(parseTraceparent("01-x-y-z")).toBeUndefined();
    expect(parseTraceparent(`00-${"a".repeat(30)}-${"b".repeat(16)}-01`)).toBeUndefined();
    expect(parseTraceparent(`00-${"z".repeat(32)}-${"b".repeat(16)}-01`)).toBeUndefined();
  });
});

describe("RequestLogger lifecycle", () => {
  it("emit posts a valid WideEvent with default success event_name", async () => {
    const { logger, captured } = captureLogger();
    const r = logger.withRequest({ traceId: "a".repeat(32) });
    r.set({ user: { id: "u1", tier: "pro", region: "eu", vip: true }, request: { flow: "checkout" } });
    r.emit();
    await logger.flush();

    expect(captured).toHaveLength(1);
    const ev = captured[0]!;
    expect(ev.schema_version).toBe("1.1");
    expect(ev.event_name).toBe("svc.request");
    expect(ev.outcome.success).toBe(true);
    expect(ev.user.id).toBe("u1");
    expect(ev.request.trace_id).toBe("a".repeat(32));
  });

  it("error() + emit() produces svc.error with structured error", async () => {
    const { logger, captured } = captureLogger();
    const r = logger.withRequest();
    r.error({ code: "PMT_502", message: "upstream timed out", path: "stripe.charge", reason: "rate_limited" });
    r.emit({ success: false, status_code: 502, kind: "http" });
    await logger.flush();

    const ev = captured[0]!;
    expect(ev.event_name).toBe("svc.error");
    expect(ev.outcome.success).toBe(false);
    expect(ev.outcome.status_code).toBe(502);
    expect(ev.error?.code).toBe("PMT_502");
    expect(ev.error?.path).toBe("stripe.charge");
  });

  it("emit is idempotent", async () => {
    const { logger, captured } = captureLogger();
    const r = logger.withRequest();
    r.emit();
    r.emit();
    await logger.flush();
    expect(captured).toHaveLength(1);
  });

  it("accepts a plain Error and a string via .error()", async () => {
    const { logger, captured } = captureLogger();
    const r1 = logger.withRequest();
    r1.error(new Error("boom")).emit({ success: false, status_code: 500, kind: "http" });
    const r2 = logger.withRequest();
    r2.error("oops").emit({ success: false, status_code: 500, kind: "http" });
    await logger.flush();

    expect(captured[0]?.error?.message).toBe("boom");
    expect(captured[1]?.error?.message).toBe("oops");
    expect(captured[0]?.error?.code).toBe("UNKNOWN");
  });

  it("traceparent() round-trips the trace and span IDs", () => {
    const { logger } = captureLogger();
    const r = logger.withRequest({ traceId: "c".repeat(32), spanId: "d".repeat(16) });
    expect(r.traceparent()).toBe(`00-${"c".repeat(32)}-${"d".repeat(16)}-01`);
  });

  it("propagates parentSpanId and parentRequestId into the emitted event", async () => {
    const { logger, captured } = captureLogger();
    logger.withRequest({ parentSpanId: "f".repeat(16), parentRequestId: "req_parent" }).emit();
    await logger.flush();
    expect(captured[0]?.request.parent_span_id).toBe("f".repeat(16));
    expect(captured[0]?.parent_request_id).toBe("req_parent");
  });
});
