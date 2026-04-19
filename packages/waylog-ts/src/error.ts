import type { ErrorContext } from "./types.js";

// createError builds a structured ErrorContext with the four schema 1.1 fields.
// Callers pass what they know; `code` and `message` are required.
export function createError(input: {
  code: string;
  message: string;
  path?: string;
  reason?: string;
}): ErrorContext {
  if (!input.code) throw new Error("createError: code is required");
  if (!input.message) throw new Error("createError: message is required");
  const out: ErrorContext = { code: input.code, message: input.message };
  if (input.path) out.path = input.path;
  if (input.reason) out.reason = input.reason;
  return out;
}

export function isErrorContext(x: unknown): x is ErrorContext {
  return (
    typeof x === "object" &&
    x !== null &&
    typeof (x as ErrorContext).code === "string" &&
    typeof (x as ErrorContext).message === "string"
  );
}
