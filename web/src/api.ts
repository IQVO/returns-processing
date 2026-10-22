import { RETURNS_API_BASE } from "./config";
import type {
  AuthorizeReturnBody,
  Disposition,
  DispositionPolicy,
  InspectionResult,
  InspectReturnLineBody,
  OpenReturnReceiptBody,
  OverrideDispositionBody,
  Return,
  ReturnList,
  Receipt,
} from "./types";

/** RFC 7807 problem+json body every error response from returns-processing
 *  returns (`type` = https://errors.returns-processing.warehouse-systems.dev/<slug>). */
export interface ProblemDetails {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  instance?: string;
}

/**
 * Every failed call -- an HTTP error with a problem+json body, an HTTP error
 * without one, or a network failure -- surfaces as one ApiError, so a screen
 * has a single shape to render: `title` (the problem's category) and `detail`
 * (what exactly was wrong), plus `slug` to branch on a specific problem
 * (e.g. `return-not-authorizable`, `open-rma-exists`).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: ProblemDetails | null;
  readonly title: string;
  readonly detail: string;
  /** Last path segment of problem.type (e.g. "open-rma-exists"); "" when unknown. */
  readonly slug: string;

  constructor(status: number, problem: ProblemDetails | null, fallbackTitle: string) {
    const title = problem?.title || fallbackTitle;
    const detail = problem?.detail ?? "";
    super(detail || title);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
    this.title = title;
    this.detail = detail;
    this.slug = problem?.type ? (problem.type.split("/").pop() ?? "") : "";
  }
}

/** Normalizes anything thrown by a request into an ApiError. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError(0, { detail: message }, "Network error");
}

/**
 * A fresh RFC 4122 v4 UUID for an Idempotency-Key header: minted once per
 * POST. `crypto.randomUUID` only exists in secure contexts, and the console
 * can be opened over plain http on a non-localhost host, so fall back to
 * getRandomValues.
 */
export function newIdempotencyKey(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

type Query = Record<string, string | number | boolean | undefined>;

function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

interface RequestOptions {
  body?: unknown;
}

/**
 * Every POST (authorize, receive, inspect, override -- all of which change
 * state and REQUIRE the key in every deployed mode) carries its own
 * `Idempotency-Key`, a UUID minted for that request, so a retry or a reload
 * of the same submit can never double-apply. GETs send no custom header, so
 * they stay simple CORS requests.
 */
async function request<T>(method: "GET" | "POST", path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {};
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (method === "POST") headers["Idempotency-Key"] = newIdempotencyKey();
  let res: Response;
  try {
    res = await fetch(`${RETURNS_API_BASE}${path}`, {
      method,
      ...(Object.keys(headers).length > 0 ? { headers } : {}),
      ...(opts.body === undefined ? {} : { body: JSON.stringify(opts.body) }),
    });
  } catch (err) {
    throw toApiError(err);
  }
  if (!res.ok) {
    let problem: ProblemDetails | null = null;
    try {
      problem = (await res.json()) as ProblemDetails;
    } catch {
      // non-JSON error body -- fall through with problem = null
    }
    throw new ApiError(res.status, problem, `${res.status} ${res.statusText}`.trim());
  }
  return (await res.json()) as T;
}

const seg = encodeURIComponent;

/** GET /returns?orderRef= (the RMAs of one order, newest first; `[]` for an
 *  unknown order, never 404). */
export async function listReturns(orderRef: string): Promise<Return[]> {
  const res = await request<ReturnList>("GET", withQuery("/returns", { orderRef }));
  return res.items ?? [];
}

/** GET /returns/{rmaId} (404 rma-not-found). */
export function getReturn(rmaId: string): Promise<Return> {
  return request<Return>("GET", `/returns/${seg(rmaId)}`);
}

/**
 * POST /returns: authorize a return against a shipped order line (201).
 * 409 return-not-authorizable (LINE_NOT_MANIFESTED / RETURN_WINDOW_EXPIRED /
 * QUANTITY_EXCEEDS_POLICY) or open-rma-exists.
 */
export function authorizeReturn(body: AuthorizeReturnBody): Promise<Return> {
  return request<Return>("POST", "/returns", { body });
}

/**
 * POST /returns/{rmaId}/receipts: record the parcel's physical arrival (201).
 * Every line is MATCHED (with its own rmaId) or explicitly UNEXPECTED.
 */
export function openReturnReceipt(rmaId: string, body: OpenReturnReceiptBody): Promise<Receipt> {
  return request<Receipt>("POST", `/returns/${seg(rmaId)}/receipts`, { body });
}

/**
 * POST /receipts/{receiptId}/lines/{lineNo}/inspect: grade one line; the
 * policy disposition is decided in the same transaction and returned (200).
 * 409 line-already-inspected / inspected-exceeds-received.
 */
export function inspectReturnLine(
  receiptId: string,
  lineNo: number,
  body: InspectReturnLineBody,
): Promise<InspectionResult> {
  return request<InspectionResult>("POST", `/receipts/${seg(receiptId)}/lines/${lineNo}/inspect`, { body });
}

/**
 * POST /dispositions/{dispositionId}/override: supersede a disposition with
 * the operator's outcome and a mandatory reason (200). The original decision
 * is never mutated.
 */
export function overrideDisposition(dispositionId: string, body: OverrideDispositionBody): Promise<Disposition> {
  return request<Disposition>("POST", `/dispositions/${seg(dispositionId)}/override`, { body });
}

/** GET /disposition-policies/current (404 policy-not-found when none was ever
 *  declared). */
export function getDispositionPolicy(): Promise<DispositionPolicy> {
  return request<DispositionPolicy>("GET", "/disposition-policies/current");
}
