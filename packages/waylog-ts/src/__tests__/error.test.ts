import { describe, expect, it } from "vitest";
import { createError, isErrorContext } from "../error.js";

describe("createError", () => {
  it("requires code and message", () => {
    expect(() => createError({ code: "", message: "x" })).toThrow();
    expect(() => createError({ code: "PMT_500", message: "" })).toThrow();
  });

  it("omits path and reason when not supplied", () => {
    const e = createError({ code: "PMT_500", message: "boom" });
    expect(e).toEqual({ code: "PMT_500", message: "boom" });
  });

  it("includes path and reason when supplied", () => {
    const e = createError({
      code: "PMT_502",
      message: "upstream",
      path: "stripe.charge",
      reason: "rate_limited",
    });
    expect(e).toEqual({
      code: "PMT_502",
      message: "upstream",
      path: "stripe.charge",
      reason: "rate_limited",
    });
  });
});

describe("isErrorContext", () => {
  it("accepts valid shape", () => {
    expect(isErrorContext({ code: "x", message: "y" })).toBe(true);
  });
  it("rejects Error instance and string", () => {
    expect(isErrorContext(new Error("x"))).toBe(false);
    expect(isErrorContext("boom")).toBe(false);
  });
});
