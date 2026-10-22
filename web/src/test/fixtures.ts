import type { Disposition, DispositionPolicy, InspectionResult, Receipt, Return } from "../types";

export const RMA_ID = "rma-4d5e6f7a-8b9c-4d0e-a1f2-3b4c5d6e7f80";
export const RECEIPT_ID = "rcpt-8a9b0c1d-2e3f-4a5b-8c6d-7e8f9a0b1c2d";
export const DISPOSITION_ID = "disp-0c1d2e3f-4a5b-4e6f-9a7b-8c9d0e1f2a3b";

export function rma(over: Partial<Return> = {}): Return {
  return {
    rmaId: RMA_ID,
    orderId: "ord-7c9e2f1a",
    lineNo: 1,
    sku: "SKU-1",
    quantity: 2,
    siteId: "WH1",
    reasonCode: "DAMAGED",
    status: "AUTHORIZED",
    windowEndsAt: "2026-11-20T09:30:00Z",
    authorizedAt: "2026-10-21T09:30:00Z",
    version: 1,
    ...over,
  };
}

export function receipt(over: Partial<Receipt> = {}): Receipt {
  return {
    receiptId: RECEIPT_ID,
    rmaId: RMA_ID,
    siteId: "WH1",
    openedAt: "2026-10-21T10:00:00Z",
    lines: [
      {
        lineNo: 1,
        sku: "SKU-1",
        quantity: 2,
        kind: "MATCHED",
        rmaId: RMA_ID,
        inspected: false,
      },
    ],
    version: 1,
    ...over,
  };
}

export function disposition(over: Partial<Disposition> = {}): Disposition {
  return {
    dispositionId: DISPOSITION_ID,
    receiptId: RECEIPT_ID,
    lineNo: 1,
    sku: "SKU-1",
    quantity: 2,
    grade: "B",
    outcome: "RESTOCK",
    policyVersion: 3,
    staged: true,
    decidedAt: "2026-10-21T10:05:00Z",
    version: 1,
    ...over,
  };
}

export function inspection(over: Partial<InspectionResult> = {}): InspectionResult {
  return {
    receiptId: RECEIPT_ID,
    lineNo: 1,
    sku: "SKU-1",
    quantity: 2,
    grade: "B",
    reasonCodes: ["CUSTOMER_DAMAGE"],
    inspectedAt: "2026-10-21T10:05:00Z",
    disposition: disposition(),
    ...over,
  };
}

export function policy(over: Partial<DispositionPolicy> = {}): DispositionPolicy {
  return {
    version: 3,
    returnWindowDays: 30,
    maxUnitsPerLine: 10,
    vetoOutcome: "DISPOSE",
    rules: [
      { grade: "A", productClass: "*", outcome: "RESTOCK" },
      { grade: "B", productClass: "*", outcome: "RESTOCK" },
      { grade: "C", productClass: "*", outcome: "LIQUIDATE" },
      { grade: "SCRAP", productClass: "*", outcome: "DISPOSE" },
      { grade: "A", productClass: "Hazmat", outcome: "RTV" },
    ],
    declaredAt: "2026-10-01T09:00:00Z",
    ...over,
  };
}
