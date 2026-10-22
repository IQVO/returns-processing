import { describe, expect, it } from "vitest";
import { RETURNS_API_BASE, resolveReturnsApiBase } from "./config";

describe("resolveReturnsApiBase", () => {
  it("builds the production API base from the runtime API origin", () => {
    expect(resolveReturnsApiBase({ apiOrigin: "http://localhost:8000" }, true)).toBe(
      "http://localhost:8000/api/returns-processing",
    );
  });

  it("normalizes trailing slashes on the runtime API origin", () => {
    expect(resolveReturnsApiBase({ apiOrigin: "https://warehouse.example/" }, true)).toBe(
      "https://warehouse.example/api/returns-processing",
    );
    expect(resolveReturnsApiBase({ apiOrigin: "https://warehouse.example///" }, true)).toBe(
      "https://warehouse.example/api/returns-processing",
    );
  });

  it("fails loudly when production runtime configuration has no API origin", () => {
    expect(() => resolveReturnsApiBase({}, true)).toThrow(
      "window.__WAREHOUSE_CONFIG__.apiOrigin is required in production",
    );
    expect(() => resolveReturnsApiBase({ apiOrigin: "" }, true)).toThrow(
      "window.__WAREHOUSE_CONFIG__.apiOrigin is required in production",
    );
  });

  it("falls back to the service's own dev port outside production", () => {
    expect(resolveReturnsApiBase({}, false)).toBe("http://localhost:8080");
  });

  it("still prefers a runtime origin over the dev fallback", () => {
    expect(resolveReturnsApiBase({ apiOrigin: "http://localhost:8000" }, false)).toBe(
      "http://localhost:8000/api/returns-processing",
    );
  });
});

describe("RETURNS_API_BASE", () => {
  it("loads without /config.json (window.__WAREHOUSE_CONFIG__ absent) in a non-production build", () => {
    expect(window.__WAREHOUSE_CONFIG__).toBeUndefined();
    expect(RETURNS_API_BASE).toBe("http://localhost:8080");
  });
});
