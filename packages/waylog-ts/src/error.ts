import { WaylogError, type ErrorOpts } from "./types.js";

const reservedCodes = new Set(["WAYLOG_TIMEOUT", "WAYLOG_ABORTED", "WAYLOG_PANIC", "WAYLOG_PARTIAL"]);

export function newError(code: string, opts: ErrorOpts = {}): WaylogError | undefined {
  if (!code) throw new Error("waylog: error code is required");
  if (reservedCodes.has(code)) return undefined;
  return new WaylogError(code, opts);
}

export function isWaylogError(err: unknown): err is WaylogError {
  return err instanceof WaylogError || (typeof err === "object" && err !== null && typeof (err as WaylogError).code === "string");
}

export function isReservedCode(code: string): boolean {
  return reservedCodes.has(code);
}
