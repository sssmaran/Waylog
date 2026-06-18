import { postSignal, runtimeHooksEnabled } from "./logger.js";

const rethrownUnhandledRejections = new WeakSet<object>();

/**
 * installGlobalHandlers registers process-level handlers that post a "runtime"
 * signal when the process hits an uncaught exception or unhandled promise
 * rejection, so crashes correlate with incidents during triage.
 *
 * No-op unless runtime hooks are enabled in config. Returns an uninstall
 * function.
 *
 * The exception handler uses `uncaughtExceptionMonitor`, which observes the
 * error without preventing Node's default crash — an observability SDK must not
 * silently keep a broken process alive. The signal is best-effort: Node may exit
 * before the async POST resolves, so callers should not rely on it landing after
 * a hard crash.
 */
export function installGlobalHandlers(): () => void {
  if (!runtimeHooksEnabled()) {
    return () => {};
  }

  const onException = (err: Error): void => {
    if (rethrownUnhandledRejections.has(err)) return;
    void emit("uncaught_exception", err);
  };
  const rethrowUnhandledRejections =
    process.listenerCount("unhandledRejection") === 0 && nodeUnhandledRejectionsModeCrashes();
  const onRejection = (reason: unknown): void => {
    const posted = emit("unhandled_rejection", reason);
    if (rethrowUnhandledRejections) {
      void posted.finally(() => {
        setImmediate(() => {
          throw markRethrown(reason);
        });
      });
    }
  };

  process.on("uncaughtExceptionMonitor", onException);
  process.on("unhandledRejection", onRejection);

  return () => {
    process.off("uncaughtExceptionMonitor", onException);
    process.off("unhandledRejection", onRejection);
  };
}

function emit(subtype: string, reason: unknown): Promise<void> {
  return postSignal({
    type: "runtime",
    service: "",
    env: "",
    severity: "critical",
    reason: `${subtype}: ${reasonText(reason)}`,
    message: stackOf(reason),
    source: "ts-sdk",
    metadata: { subtype },
  }).catch(() => {});
}

function reasonText(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

function stackOf(reason: unknown): string {
  return reason instanceof Error && reason.stack ? reason.stack : String(reason);
}

function markRethrown(reason: unknown): unknown {
  if (typeof reason === "object" && reason !== null) {
    rethrownUnhandledRejections.add(reason);
    return reason;
  }
  const err = new Error(String(reason));
  rethrownUnhandledRejections.add(err);
  return err;
}

function nodeUnhandledRejectionsModeCrashes(): boolean {
  const flag = process.execArgv.find((arg) => arg.startsWith("--unhandled-rejections"));
  const mode = flag?.includes("=") ? flag.split("=", 2)[1] : undefined;
  return mode !== "warn" && mode !== "none";
}
