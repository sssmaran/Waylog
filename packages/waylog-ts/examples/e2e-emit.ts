// e2e-emit.ts is a low-level schema 2.0 smoke driver, not the recommended
// user-facing integration example. Real apps should usually use framework
// middleware (`@waylog/sdk/express`, `@waylog/sdk/hono`, etc.) and
// `useLogger(...)`. This file exercises the standalone core directly so the
// e2e path can emit a deterministic payment-failure event without depending
// on any framework adapter.

import { begin, fail, finalize, from, init, newError, recordOutgoingSpan, setField, setHTTPStatus, stepSync, traceId } from "../src/index.js";

init({
  ingestUrl: process.env.INGEST_URL ?? "http://localhost:8080",
  service: "ts-e2e",
  env: "dev",
  version: "0.1.0",
});

const ctx = begin({});
setField(ctx, "http", { method: "POST", route: "/api/e2e", status: 200 });
setField(ctx, "user", { id: "ts-e2e-user", tier: "standard", region: "us-east-1" });

stepSync(ctx, "db.load_cart", () => undefined);
const err = newError("PMT_502", { reason: "upstream gateway 5xx" });
try {
  stepSync(ctx, "payment.charge", () => {
    from(ctx).warn("retrying payment");
    recordOutgoingSpan(ctx, "3333333333333333", "payment", "POST /charge");
    fail(ctx, err);
    throw err;
  });
} catch {
  setHTTPStatus(ctx, 502);
}

await finalize(ctx);

console.log(traceId(ctx));
