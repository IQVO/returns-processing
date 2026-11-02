/**
 * Wire shapes of returns-processing's REST API (apis/openapi.yaml; the JSON
 * field names are the truth in internal/adapters/inbound/http/dto.go).
 * JSON is camelCase; optional parts of a resource are OMITTED, never null
 * (the one exception: Receipt.rmaId, which is explicitly nullable).
 */

export const RETURN_STATUSES = ["REQUESTED", "AUTHORIZED", "REJECTED", "RECEIVED", "CLOSED"] as const;
export type ReturnStatus = (typeof RETURN_STATUSES)[number];

export const GRADES = ["A", "B", "C", "SCRAP"] as const;
export type Grade = (typeof GRADES)[number];

export const OUTCOMES = ["RESTOCK", "REFURBISH", "LIQUIDATE", "DISPOSE", "RTV"] as const;
export type Outcome = (typeof OUTCOMES)[number];

export const LINE_KINDS = ["MATCHED", "UNEXPECTED"] as const;
export type LineKind = (typeof LINE_KINDS)[number];

/** One RMA. `reasonCode` on an AUTHORIZED one, `rejectionCode` on a REJECTED
 *  one; the timestamps appear with the lifecycle. */
export interface Return {
  rmaId: string;
  orderId: string;
  lineNo: number;
  sku: string;
  quantity: number;
  siteId: string;
  reasonCode?: string;
  rejectionCode?: string;
  status: ReturnStatus;
  windowEndsAt?: string;
  authorizedAt?: string;
  rejectedAt?: string;
  receivedAt?: string;
  closedAt?: string;
  version: number;
}

/** One page of GET /returns: `nextCursor` is absent on the last page. */
export interface ReturnPage {
  items: Return[];
  nextCursor?: string;
}

/** The body of POST /returns. */
export interface AuthorizeReturnBody {
  orderId: string;
  lineNo: number;
  sku: string;
  quantity: number;
  siteId: string;
  reasonCode: string;
}

/** One line of the body of POST /returns/{rmaId}/receipts. A MATCHED line
 *  carries the rmaId it matches (which may differ from the path's RMA when
 *  several RMAs ship in one parcel); an UNEXPECTED line carries none. */
export interface OpenReceiptLineBody {
  lineNo: number;
  sku: string;
  quantity: number;
  kind: LineKind;
  rmaId?: string;
}

export interface OpenReturnReceiptBody {
  siteId: string;
  lines: OpenReceiptLineBody[];
}

export interface ReceiptLine {
  lineNo: number;
  sku: string;
  quantity: number;
  kind: LineKind;
  rmaId?: string;
  inspected: boolean;
  inspectedQuantity?: number;
  grade?: Grade;
  reasonCodes?: string[];
  inspectedAt?: string;
}

export interface Receipt {
  receiptId: string;
  /** Null when the whole parcel was unexpected. */
  rmaId: string | null;
  siteId: string;
  openedAt: string;
  lines: ReceiptLine[];
  version: number;
}

/** The body of POST /receipts/{receiptId}/lines/{lineNo}/inspections. `quantity`
 *  omitted = the line's full received quantity. */
export interface InspectReturnLineBody {
  grade: Grade;
  quantity?: number;
  reasonCodes: string[];
}

export interface Disposition {
  dispositionId: string;
  receiptId: string;
  lineNo: number;
  sku: string;
  quantity: number;
  grade: Grade;
  outcome: Outcome;
  policyVersion: number;
  supersedes?: string;
  reason?: string;
  staged: boolean;
  decidedAt: string;
  version: number;
}

/** The response of the inspect call: the graded line and, decided in the same
 *  transaction, the policy disposition for it. */
export interface InspectionResult {
  receiptId: string;
  lineNo: number;
  sku: string;
  quantity: number;
  grade: Grade;
  reasonCodes: string[];
  inspectedAt: string;
  disposition: Disposition;
}

/** The body of POST /dispositions/{dispositionId}/overrides. */
export interface OverrideDispositionBody {
  outcome: Outcome;
  reason: string;
}

export interface PolicyRule {
  grade: Grade;
  /** "*" is the required per-grade default, which makes Decide total. */
  productClass: string;
  outcome: Outcome;
}

export interface DispositionPolicy {
  version: number;
  returnWindowDays: number;
  maxUnitsPerLine: number;
  vetoOutcome?: Outcome;
  rules: PolicyRule[];
  declaredAt: string;
}
