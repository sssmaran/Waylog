// e2e-emit.ts emits a single TS-SDK trace (1 request) to the ingest server
// and prints the trace_id on stdout. Used by scripts/e2e-mark2.sh to drive
// the TypeScript SDK path.
//
// Runner: vite-node (available in local node_modules/.bin after `npm install`
// inside packages/waylog-ts — ships with vitest).

import { createLogger } from "../src/index.js";

const endpoint = process.env.INGEST_URL ?? "http://localhost:8080";

const logger = createLogger({
  endpoint,
  service: "ts-e2e",
  env: "dev",
  version: "0.1.0",
  batchSize: 1,
  flushIntervalMs: 100,
});

const req = logger.withRequest({
  flow: "purchase",
  user: { id: "ts-e2e-user", tier: "standard", region: "us-east-1" },
  httpMethod: "GET",
  routeTemplate: "/api/e2e",
});

// Extract the trace_id from the outbound traceparent: "00-<trace>-<span>-<flags>".
const traceId = req.traceparent().split("-")[1]!;

req.emit({ success: true, status_code: 200 });

await logger.flush();
await logger.close();

console.log(traceId);
