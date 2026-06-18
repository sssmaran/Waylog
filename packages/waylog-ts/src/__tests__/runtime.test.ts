import { describe, expect, it, afterEach, vi } from "vitest";
import { init, shutdown, postSignal, installGlobalHandlers } from "../index.js";

type AnyListener = (...args: unknown[]) => void;

function initWithHooks(enabled: boolean, fetchMock: typeof fetch) {
  init({
    service: "checkout",
    env: "demo",
    ingestUrl: "http://localhost:8080",
    apiKey: "k1",
    enableRuntimeHooks: enabled,
    fetch: fetchMock,
  });
}

function okFetch() {
  return vi.fn(async () => new Response(null, { status: 201 })) as unknown as typeof fetch;
}

function calls(fetchMock: typeof fetch) {
  return (fetchMock as unknown as ReturnType<typeof vi.fn>).mock.calls;
}

// captureHandlers installs the global hooks while spying on process.on so we can
// invoke the registered listeners directly. Calling them avoids process.emit(),
// which would trip vitest's own unhandledRejection listener and fail the run.
function captureHandlers(): { handlers: Record<string, AnyListener>; uninstall: () => void } {
  const onSpy = vi.spyOn(process, "on");
  const uninstall = installGlobalHandlers();
  const handlers: Record<string, AnyListener> = {};
  for (const call of onSpy.mock.calls) {
    handlers[call[0] as string] = call[1] as AnyListener;
  }
  onSpy.mockRestore();
  return { handlers, uninstall };
}

async function flush() {
  await new Promise((r) => setTimeout(r, 0));
}

afterEach(async () => {
  await shutdown(0);
});

describe("postSignal", () => {
  it("posts to /v1/signals with bearer auth and config service/env/timestamp", async () => {
    const fetchMock = okFetch();
    initWithHooks(true, fetchMock);

    await postSignal({
      type: "runtime",
      service: "",
      env: "",
      severity: "critical",
      reason: "panic: boom",
      source: "ts-sdk",
      metadata: { subtype: "panic" },
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, opts] = calls(fetchMock)[0];
    expect(url).toBe("http://localhost:8080/v1/signals");
    expect((opts as RequestInit).headers).toMatchObject({ Authorization: "Bearer k1" });
    const body = JSON.parse((opts as RequestInit).body as string);
    expect(body.service).toBe("checkout");
    expect(body.env).toBe("demo");
    expect(body.timestamp).toBeTruthy();
    expect(body.metadata.subtype).toBe("panic");
  });
});

describe("installGlobalHandlers", () => {
  it("posts an unhandled_rejection runtime signal with env from config", async () => {
    const fetchMock = okFetch();
    initWithHooks(true, fetchMock);
    const existingHandler = () => {};
    process.on("unhandledRejection", existingHandler);
    const { handlers, uninstall } = captureHandlers();
    try {
      handlers["unhandledRejection"]!(new Error("boom"), Promise.resolve());
      await flush();
    } finally {
      uninstall();
      process.off("unhandledRejection", existingHandler);
    }

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const body = JSON.parse((calls(fetchMock)[0][1] as RequestInit).body as string);
    expect(body.type).toBe("runtime");
    expect(body.source).toBe("ts-sdk");
    expect(body.env).toBe("demo");
    expect(body.metadata.subtype).toBe("unhandled_rejection");
  });

  it("posts an uncaught_exception runtime signal", async () => {
    const fetchMock = okFetch();
    initWithHooks(true, fetchMock);
    const { handlers, uninstall } = captureHandlers();
    try {
      handlers["uncaughtExceptionMonitor"]!(new Error("kaboom"));
      await flush();
    } finally {
      uninstall();
    }

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const body = JSON.parse((calls(fetchMock)[0][1] as RequestInit).body as string);
    expect(body.metadata.subtype).toBe("uncaught_exception");
  });

  it("is a no-op when runtime hooks are disabled", () => {
    const fetchMock = okFetch();
    initWithHooks(false, fetchMock);
    const { handlers, uninstall } = captureHandlers();
    uninstall();

    expect(handlers["unhandledRejection"]).toBeUndefined();
    expect(handlers["uncaughtExceptionMonitor"]).toBeUndefined();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
