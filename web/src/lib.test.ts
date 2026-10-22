import { describe, expect, it } from "vitest";
import { idProblem, positiveWhole, reasonCodes, utcLabel, wholeNumber } from "./lib";

describe("utcLabel", () => {
  it("formats an RFC 3339 instant to minute precision", () => {
    expect(utcLabel("2026-10-17T09:30:00Z")).toBe("2026-10-17 09:30Z");
  });
  it("passes unparseable text through", () => {
    expect(utcLabel("not-a-date")).toBe("not-a-date");
    expect(utcLabel(undefined)).toBe("—");
  });
});

describe("wholeNumber / positiveWhole", () => {
  it("accepts whole numbers only", () => {
    expect(wholeNumber("12")).toBe(12);
    expect(wholeNumber(" 0 ")).toBe(0);
    expect(wholeNumber("12.5")).toBeNull();
    expect(wholeNumber("1e3")).toBeNull();
    expect(wholeNumber("-1")).toBeNull();
    expect(wholeNumber("")).toBeNull();
  });
  it("positiveWhole rejects zero", () => {
    expect(positiveWhole("1")).toBe(1);
    expect(positiveWhole("0")).toBeNull();
    expect(positiveWhole("x")).toBeNull();
  });
});

describe("idProblem", () => {
  it("requires a non-blank id of at most 64 characters", () => {
    expect(idProblem("order id", "ord-1")).toBeNull();
    expect(idProblem("order id", " ")).toBe("An order id is required.");
    expect(idProblem("SKU", "x".repeat(65))).toBe("A SKU is at most 64 characters.");
  });
});

describe("reasonCodes", () => {
  it("splits a comma-separated list, dropping blanks", () => {
    expect(reasonCodes("CUSTOMER_DAMAGE, OPENED_BOX ,")).toEqual(["CUSTOMER_DAMAGE", "OPENED_BOX"]);
  });
  it("collapses inner whitespace to underscores", () => {
    expect(reasonCodes("customer damage, opened box")).toEqual(["customer_damage", "opened_box"]);
  });
  it("is null when nothing parseable is left", () => {
    expect(reasonCodes("")).toBeNull();
    expect(reasonCodes(" , ")).toBeNull();
  });
});
