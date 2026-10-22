import { NO_VALUE } from "@warehouse/ui-kit";

/** "2026-10-17 09:30Z" for an RFC 3339 instant; the raw text when unparseable. */
export function utcLabel(iso: string | undefined): string {
  if (!iso) return NO_VALUE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t).toISOString();
  return `${d.slice(0, 10)} ${d.slice(11, 16)}Z`;
}

/**
 * A strictly whole number typed into a field (zero allowed), or null when the
 * text is empty or not a whole number ("12.5", "1e3", "abc", "-1").
 */
export function wholeNumber(raw: string): number | null {
  const s = raw.trim();
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return Number.isSafeInteger(n) ? n : null;
}

/** A strictly whole, positive number, or null ("0" is not positive). */
export function positiveWhole(raw: string): number | null {
  const n = wholeNumber(raw);
  return n !== null && n > 0 ? n : null;
}

/** An id of the API (order id, SKU, site id, reason code): 1..64 characters
 *  and not blank (the API rejects blank or over-long values). */
export function idProblem(what: IdKind, value: string): string | null {
  const article = /^[aeiou]/i.test(what) ? "An" : "A";
  if (value.trim().length === 0) return `${article} ${what} is required.`;
  if (value.length > 64) return `${article} ${what} is at most 64 characters.`;
  return null;
}

export type IdKind = "order id" | "SKU" | "site id" | "return reason" | "RMA id";

/** "DAMAGED, WRONG_ITEM" -> ["DAMAGED", "WRONG_ITEM"]: inspection reasons are
 *  typed as a comma-separated list; empty segments are dropped. Returns null
 *  when nothing parseable is left (the API requires a non-empty array). */
export function reasonCodes(raw: string): string[] | null {
  const codes = raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
    .map((s) => s.replace(/\s+/g, "_"));
  return codes.length > 0 ? codes : null;
}
