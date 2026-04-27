import { describe, expect, it } from "vitest";
import {
  begin,
  explain,
  fail,
  finalize,
  finalizeAborted,
  finalizePanic,
  finalizeTimeout,
  from,
  init,
  newError,
  parseTraceparent,
  recordOutgoingSpan,
  setField,
  setHTTPStatus,
  shutdown,
  stats,
  stepSync,
  suppress,
} from "../index.js";
import type { WideEvent } from "../types.js";

function harness() {
  const lines: string[] = [];
  init({ service: "checkout", env: "test", output: (line) => lines.push(line) });
  return {
    lines,
    event(): WideEvent {
      const json = lines.filter((line) => line.trim().startsWith("{")).at(-1);
      if (!json) throw new Error("no event emitted");
      return JSON.parse(json) as WideEvent;
    },
  };
}

describe("v2 core lifecycle", () => {
  it("emits an ok event with steps and fields", async () => {
    const h = harness();
    const ctx = begin({}, { traceId: "1".repeat(32), spanId: "1".repeat(16) });
    setField(ctx, "http", { method: "POST", route: "/checkout", status: 200 });
    stepSync(ctx, "db.load_cart", () => undefined);
    await finalize(ctx);

    const ev = h.event();
    expect(ev.schema_version).toBe("2.0");
    expect(ev.status).toBe("ok");
    expect(ev.steps?.[0]?.name).toBe("db.load_cart");
    expect((ev.fields?.http as any).route).toBe("/checkout");
  });

  it("records explicit failures and local explain output", async () => {
    harness();
    const ctx = begin({}, { traceId: "2".repeat(32), spanId: "2".repeat(16) });
    const err = newError("PMT_502", { reason: "upstream gateway 5xx" })!;
    expect(() => stepSync(ctx, "payment.charge", () => {
      fail(ctx, err);
      throw err;
    })).toThrow("upstream gateway 5xx");
    setHTTPStatus(ctx, 502);
    const explained = await explain(ctx);
    await finalize(ctx);

    expect(explained.anchor?.error_code).toBe("PMT_502");
    expect(explained.toString()).toContain("PMT_502");
  });

  it("suppressed events keep fields but drop detail", async () => {
    const h = harness();
    const ctx = begin({});
    setField(ctx, "http", { method: "GET", route: "/healthz", status: 200 });
    suppress(ctx);
    await finalize(ctx);

    const ev = h.event();
    expect(ev.status).toBe("suppressed");
    expect(ev.fields?.http).toEqual({ method: "GET", route: "/healthz", status: 200 });
    expect(ev.steps).toBeUndefined();
  });

  it("panic records errors[] while timeout and abort only anchor", async () => {
    const panicH = harness();
    const panicCtx = begin({});
    await finalizePanic(panicCtx);
    expect(panicH.event().errors?.[0]?.code).toBe("WAYLOG_PANIC");

    const timeoutH = harness();
    const timeoutCtx = begin({});
    await finalizeTimeout(timeoutCtx);
    expect(timeoutH.event().anchor?.error_code).toBe("WAYLOG_TIMEOUT");
    expect(timeoutH.event().errors).toBeUndefined();

    const abortedH = harness();
    const abortedCtx = begin({});
    await finalizeAborted(abortedCtx);
    expect(abortedH.event().anchor?.error_code).toBe("WAYLOG_ABORTED");
    expect(abortedH.event().errors).toBeUndefined();
  });

  it("timeout snapshots an active step", async () => {
    const h = harness();
    const ctx = begin({});
    stepSync(ctx, "payment.charge", () => {
      recordOutgoingSpan(ctx, "3".repeat(16), "payment", "POST /charge");
      void finalizeTimeout(ctx);
    });

    const ev = h.event();
    expect(ev.status).toBe("timeout");
    expect(ev.anchor?.step).toBe("payment.charge");
    expect(ev.steps?.[0]?.name).toBe("payment.charge");
    expect(ev.steps?.[0]?.downstream?.service).toBe("payment");
  });

  it("panic and timeout preserve explicit failure anchors", async () => {
    const panicH = harness();
    const panicCtx = begin({});
    fail(panicCtx, newError("PMT_502", { reason: "payment failed first" }));
    await finalizePanic(panicCtx);
    expect(panicH.event().anchor?.error_code).toBe("PMT_502");
    expect(panicH.event().status).toBe("error");

    const timeoutH = harness();
    const timeoutCtx = begin({});
    fail(timeoutCtx, newError("PMT_502", { reason: "payment failed first" }));
    await finalizeTimeout(timeoutCtx);
    expect(timeoutH.event().anchor?.error_code).toBe("PMT_502");
    expect(timeoutH.event().status).toBe("timeout");
  });

  it("panic finalization replaces synthetic step panic anchors with WAYLOG_PANIC", async () => {
    const h = harness();
    const ctx = begin({});
    expect(() => stepSync(ctx, "payment.charge", () => {
      throw new Error("boom");
    })).toThrow("boom");
    await finalizePanic(ctx);
    expect(h.event().anchor).toEqual({ step: "payment.charge", error_code: "WAYLOG_PANIC" });
  });

  it("reserved error codes are rejected without throwing", () => {
    harness();
    expect(newError("WAYLOG_TIMEOUT")).toBeUndefined();
    expect(stats().reservedCodeRejections).toBe(1);
  });

  it("rate limiting drops ok before priority", async () => {
    const lines: string[] = [];
    init({ service: "checkout", env: "test", output: (line) => lines.push(line), maxEventsPerSec: 1 });
    await finalize(begin({}));
    await finalize(begin({}));
    const failed = begin({});
    fail(failed, newError("BOOM"));
    await finalize(failed);
    expect(lines.filter((line) => line.trim().startsWith("{"))).toHaveLength(2);
    expect(stats().eventsDropped).toBe(1);
  });

  it("dev mode emits pretty log lines and final JSON", async () => {
    const lines: string[] = [];
    init({ service: "checkout", env: "dev", output: (line) => lines.push(line) });
    const ctx = begin({});
    from(ctx).warn("retrying payment", { cart_id: "c_1" });
    await finalize(ctx);
    expect(lines.some((line) => line.includes("[WARN] retrying payment"))).toBe(true);
    expect(lines.some((line) => line.includes("\n  \"schema_version\""))).toBe(true);
  });

  it("redacts final fields synchronously", async () => {
    const h = (() => {
      const lines: string[] = [];
      init({
        service: "checkout",
        env: "test",
        output: (line) => lines.push(line),
        redactor: (fields) => ({ ...fields, token: "[REDACTED]" }),
      });
      return { event: () => JSON.parse(lines.find((line) => line.trim().startsWith("{"))!) as WideEvent };
    })();
    const ctx = begin({});
    setField(ctx, "token", "secret");
    await finalize(ctx);
    expect(h.event().fields?.token).toBe("[REDACTED]");
  });

  it("caps buffered logs without rejecting request finalization", async () => {
    const h = (() => {
      const lines: string[] = [];
      init({ service: "checkout", env: "test", output: (line) => lines.push(line), maxLogs: 1 });
      return { event: () => JSON.parse(lines.find((line) => line.trim().startsWith("{"))!) as WideEvent };
    })();
    const ctx = begin({});
    from(ctx).info("one");
    from(ctx).info("two");
    await finalize(ctx);
    expect(h.event().logs).toHaveLength(1);
    expect(stats().logsDropped).toBe(1);
  });
});

describe("traceparent", () => {
  it("parses valid W3C traceparent", () => {
    expect(parseTraceparent(`00-${"a".repeat(32)}-${"b".repeat(16)}-01`)).toEqual({ traceId: "a".repeat(32), spanId: "b".repeat(16) });
  });

  it("rejects invalid traceparent", () => {
    expect(parseTraceparent("bad")).toBeUndefined();
  });
});
