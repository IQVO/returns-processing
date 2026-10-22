import { describe, expect, it } from "vitest";
import {
  ApiError,
  authorizeReturn,
  getDispositionPolicy,
  getReturn,
  inspectReturnLine,
  listReturns,
  newIdempotencyKey,
  openReturnReceipt,
  overrideDisposition,
  toApiError,
} from "./api";
import { json, mockApi, problem } from "./test/fetchMock";
import { DISPOSITION_ID, RECEIPT_ID, RMA_ID, disposition, inspection, policy, receipt, rma } from "./test/fixtures";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe("api", () => {
  it("sends the orderRef query and no custom header on a GET", async () => {
    const api = mockApi({
      "GET /returns": json({ items: [rma()] }),
      [`GET /returns/${RMA_ID}`]: json(rma()),
      "GET /disposition-policies/current": json(policy()),
    });
    const items = await listReturns("ord-7c9e2f1a");
    expect(items).toHaveLength(1);
    const [list] = api.to("GET /returns");
    expect(list.query.get("orderRef")).toBe("ord-7c9e2f1a");
    expect(list.headers).toEqual({});
    await getReturn(RMA_ID);
    expect(api.to(`GET /returns/${RMA_ID}`)).toHaveLength(1);
    await getDispositionPolicy();
    expect(api.to("GET /disposition-policies/current")).toHaveLength(1);
  });

  it("mints a fresh Idempotency-Key per POST, on every write", async () => {
    const api = mockApi({
      "POST /returns": json(rma(), 201),
      [`POST /returns/${RMA_ID}/receipts`]: json(receipt(), 201),
      [`POST /receipts/${RECEIPT_ID}/lines/1/inspect`]: json(inspection()),
      [`POST /dispositions/${DISPOSITION_ID}/override`]: json(disposition({ outcome: "RTV", supersedes: DISPOSITION_ID })),
    });
    await authorizeReturn({ orderId: "ord-7c9e2f1a", lineNo: 1, sku: "SKU-1", quantity: 2, siteId: "WH1", reasonCode: "DAMAGED" });
    await openReturnReceipt(RMA_ID, { siteId: "WH1", lines: [{ lineNo: 1, sku: "SKU-1", quantity: 2, kind: "MATCHED", rmaId: RMA_ID }] });
    await inspectReturnLine(RECEIPT_ID, 1, { grade: "B", reasonCodes: ["CUSTOMER_DAMAGE"] });
    await overrideDisposition(DISPOSITION_ID, { outcome: "RTV", reason: "vendor accepts open-box" });
    const keys = api.calls.filter((c) => c.method === "POST").map((c) => c.headers["Idempotency-Key"]);
    expect(keys).toHaveLength(4);
    for (const k of keys) expect(k).toMatch(UUID);
    expect(new Set(keys).size).toBe(4);
    // GETs never carry one.
    expect(api.calls.filter((c) => c.method === "GET").every((c) => c.headers["Idempotency-Key"] === undefined)).toBe(true);
  });

  it("builds the exact write bodies and paths of the API", async () => {
    const api = mockApi({
      "POST /returns": json(rma(), 201),
      [`POST /returns/${RMA_ID}/receipts`]: json(receipt(), 201),
      [`POST /receipts/${RECEIPT_ID}/lines/2/inspect`]: json(inspection({ lineNo: 2 })),
      [`POST /dispositions/${DISPOSITION_ID}/override`]: json(disposition()),
    });
    await authorizeReturn({ orderId: "ord-1", lineNo: 3, sku: "SKU-9", quantity: 1, siteId: "WH2", reasonCode: "WRONG_ITEM" });
    expect(api.to("POST /returns")[0].body).toEqual({
      orderId: "ord-1", lineNo: 3, sku: "SKU-9", quantity: 1, siteId: "WH2", reasonCode: "WRONG_ITEM",
    });
    // An UNEXPECTED line carries no rmaId at all; the inspect quantity is
    // omitted when the caller leaves it to the line's full quantity.
    await openReturnReceipt(RMA_ID, { siteId: "WH2", lines: [{ lineNo: 1, sku: "SKU-9", quantity: 1, kind: "UNEXPECTED" }] });
    expect(api.to(`POST /returns/${RMA_ID}/receipts`)[0].body).toEqual({
      siteId: "WH2", lines: [{ lineNo: 1, sku: "SKU-9", quantity: 1, kind: "UNEXPECTED" }],
    });
    await inspectReturnLine(RECEIPT_ID, 2, { grade: "A", reasonCodes: ["OPENED_BOX"] });
    expect(api.to(`POST /receipts/${RECEIPT_ID}/lines/2/inspect`)[0].body).toEqual({
      grade: "A", reasonCodes: ["OPENED_BOX"],
    });
    await overrideDisposition(DISPOSITION_ID, { outcome: "DISPOSE", reason: "hygiene" });
    expect(api.to(`POST /dispositions/${DISPOSITION_ID}/override`)[0].body).toEqual({
      outcome: "DISPOSE", reason: "hygiene",
    });
  });

  it("surfaces a problem+json body as an ApiError with its slug", async () => {
    mockApi({ "POST /returns": problem(409, "open-rma-exists", "Refused", "one open RMA per (orderRef, lineNo)") });
    const err = await authorizeReturn({ orderId: "ord-1", lineNo: 1, sku: "SKU-1", quantity: 1, siteId: "WH1", reasonCode: "DAMAGED" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect([e.status, e.slug, e.title, e.detail]).toEqual([409, "open-rma-exists", "Refused", "one open RMA per (orderRef, lineNo)"]);
  });

  it("surfaces a non-JSON error and a network failure as ApiErrors", async () => {
    mockApi({ "GET /returns": new Response("<html>bad gateway</html>", { status: 502, statusText: "Bad Gateway" }) });
    const e1 = await listReturns("ord-1").then(() => { throw new Error("expected a rejection"); }, (e: unknown) => e as ApiError);
    expect([e1.status, e1.title, e1.slug]).toEqual([502, "502 Bad Gateway", ""]);
    mockApi({});
    const e2 = await listReturns("ord-1").then(() => { throw new Error("expected a rejection"); }, (e: unknown) => e as ApiError);
    expect([e2.status, e2.title]).toEqual([0, "Network error"]);
    expect(toApiError("boom").detail).toBe("boom");
  });

  it("newIdempotencyKey is a v4 UUID", () => {
    expect(newIdempotencyKey()).toMatch(UUID);
  });
});
