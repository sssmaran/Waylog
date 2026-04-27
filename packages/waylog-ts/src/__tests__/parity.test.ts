import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { begin, fail, finalize, finalizeAborted, finalizePanic, finalizeTimeout, from, init, newError, recordOutgoingSpan, setField, setHTTPStatus, stepSync, suppress } from "../index.js";
import type { WideEvent } from "../types.js";

const fixtureDir = join(process.cwd(), "..", "..", "testdata", "fixtures", "v2");

function capture() {
  const lines: string[] = [];
  init({ service: "checkout", env: "test", output: (line) => lines.push(line) });
  return {
    event(): WideEvent {
      const raw = lines.find((line) => line.trim().startsWith("{"));
      if (!raw) throw new Error("no event");
      return JSON.parse(raw) as WideEvent;
    },
  };
}

function fixture(name: string): WideEvent {
  return JSON.parse(readFileSync(join(fixtureDir, name), "utf8")) as WideEvent;
}

function masked(ev: WideEvent): unknown {
  const v = JSON.parse(JSON.stringify(ev)) as any;
  for (const k of ["event_id", "trace_id", "span_id", "parent_span_id", "ts_start", "ts_end"]) {
    if (k in v) v[k] = v[k] === "" ? "__MASKED_EMPTY__" : "__MASKED__";
  }
  v.duration_ms = 0;
  for (const s of v.steps ?? []) {
    if ("span_id" in s) s.span_id = s.span_id === "" ? "__MASKED_EMPTY__" : "__MASKED__";
    s.start_ms = 0;
    s.duration_ms = 0;
  }
  for (const l of v.logs ?? []) l.ts_offset_ms = 0;
  return normalize(v);
}

function normalize(v: unknown): unknown {
  if (Array.isArray(v)) return v.length === 0 ? undefined : v.map(normalize).filter((x) => x !== undefined);
  if (v && typeof v === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, val] of Object.entries(v)) {
      const n = normalize(val);
      if (n !== undefined && n !== "") out[k] = n;
    }
    return Object.keys(out).length === 0 ? undefined : out;
  }
  return v;
}

function expectParity(got: WideEvent, name: string): void {
  expect(masked(got)).toEqual(masked(fixture(name)));
}

describe("TS fixture parity", () => {
  it("ok-simple", async () => {
    const h = capture();
    const ctx = begin({}, { traceId: "1".repeat(32), spanId: "1".repeat(16) });
    setField(ctx, "http", { method: "POST", route: "/checkout", status: 200 });
    stepSync(ctx, "db.load_cart", () => undefined);
    await finalize(ctx);
    expectParity(h.event(), "ok-simple.json");
  });

  it("error-payment-cascade", async () => {
    const h = capture();
    const ctx = begin({}, { traceId: "2".repeat(32), spanId: "2".repeat(16) });
    setField(ctx, "http", { method: "POST", route: "/checkout", status: 200 });
    setField(ctx, "user", { id: "u_123" });
    stepSync(ctx, "db.load_cart", () => undefined);
    const err = newError("PMT_502", { reason: "upstream gateway 5xx" });
    try {
      stepSync(ctx, "payment.charge", () => {
        from(ctx).warn("retrying payment");
        recordOutgoingSpan(ctx, "3".repeat(16), "payment", "POST /charge");
        fail(ctx, err);
        throw err;
      });
    } catch {
      // Expected by the fixture driver.
    }
    setHTTPStatus(ctx, 502);
    await finalize(ctx);
    expectParity(h.event(), "error-payment-cascade.json");
  });

  it("error-panic", async () => {
    const h = capture();
    const ctx = begin({}, { traceId: "6".repeat(32), spanId: "7".repeat(16) });
    setField(ctx, "http", { method: "POST", route: "/checkout", status: 500 });
    await finalizePanic(ctx);
    expectParity(h.event(), "error-panic.json");
  });

  it("suppressed-healthcheck", async () => {
    const h = capture();
    const ctx = begin({}, { traceId: "5".repeat(32), spanId: "6".repeat(16) });
    setField(ctx, "http", { method: "GET", route: "/healthz", status: 200 });
    suppress(ctx);
    await finalize(ctx);
    expectParity(h.event(), "suppressed-healthcheck.json");
  });

  it("aborted-cancel", async () => {
    const h = capture();
    const ctx = begin({}, { traceId: "4".repeat(32), spanId: "5".repeat(16) });
    setField(ctx, "http", { method: "POST", route: "/checkout" });
    await finalizeAborted(ctx);
    expectParity(h.event(), "aborted-cancel.json");
  });

  it("timeout-watchdog", async () => {
    const h = capture();
    const ctx = begin({}, { traceId: "3".repeat(32), spanId: "4".repeat(16) });
    setField(ctx, "http", { method: "POST", route: "/checkout" });
    stepSync(ctx, "db.load_cart", () => undefined);
    stepSync(ctx, "payment.charge", () => {
      void finalizeTimeout(ctx);
    });
    expectParity(h.event(), "timeout-watchdog.json");
  });
});
