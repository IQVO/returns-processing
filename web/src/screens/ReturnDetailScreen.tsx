import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import {
  getReturn,
  inspectReturnLine,
  openReturnReceipt,
  overrideDisposition,
  toApiError,
} from "../api";
import type { ApiError } from "../api";
import {
  Form,
  FormRow,
  InlineError,
  InlineSuccess,
  SelectField,
  SubmitButton,
  TextField,
} from "../components/formkit";
import { Facts, GradePill, OutcomePill, PageHeader, ReturnStatusPill, Stack } from "../components/layout";
import { ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { idProblem, positiveWhole, reasonCodes, utcLabel } from "../lib";
import { GRADES, OUTCOMES } from "../types";
import type { Disposition, InspectionResult, LineKind, Receipt, Return } from "../types";

/** Reads :rmaId and keys the screen on it, so moving between RMAs never
 *  carries receipt or inspection state over. */
export function ReturnDetailRoute() {
  const { rmaId = "" } = useParams();
  return <ReturnDetailScreen key={rmaId} rmaId={rmaId} />;
}

/**
 * One RMA and, from it, the whole physical reverse flow the API serves:
 * while AUTHORIZED, open the receipt the parcel arrived on (POST
 * /returns/{rmaId}/receipts), then grade each received line (POST
 * /receipts/{id}/lines/{n}/inspect), which decides the disposition in the
 * same transaction, and optionally override that decision (POST
 * /dispositions/{id}/override). Every POST carries its own Idempotency-Key.
 *
 * HONEST LIMIT (the API serves no receipt or disposition GET): the lines and
 * decisions below are what the write responses returned, kept in this screen.
 * A reload clears the panel; the RMA card above always re-reads.
 */
export function ReturnDetailScreen({ rmaId }: { rmaId: string }) {
  const rma = useRequest(() => getReturn(rmaId), `rma|${rmaId}`);
  const [receipt, setReceipt] = useState<Receipt | null>(null);
  /** lineNo -> the inspection (and its decided disposition). */
  const [inspections, setInspections] = useState<Record<number, InspectionResult>>({});
  /** dispositionId -> the superseding override. */
  const [overrides, setOverrides] = useState<Record<string, Disposition>>({});

  return (
    <Stack gap={5}>
      <div>
        <Link to="../returns">← All returns</Link>
      </div>
      <RequestView state={rma} what="return">
        {(r) => (
          <ReturnBody
            rma={r}
            receipt={receipt}
            inspections={inspections}
            overrides={overrides}
            onReceipt={(opened) => {
              setReceipt(opened);
              rma.reload();
            }}
            onInspected={(lineNo, result) => setInspections((prev) => ({ ...prev, [lineNo]: result }))}
            onOverride={(dispositionId, next) => setOverrides((prev) => ({ ...prev, [dispositionId]: next }))}
          />
        )}
      </RequestView>
    </Stack>
  );
}

function ReturnBody({
  rma,
  receipt,
  inspections,
  overrides,
  onReceipt,
  onInspected,
  onOverride,
}: {
  rma: Return;
  receipt: Receipt | null;
  inspections: Record<number, InspectionResult>;
  overrides: Record<string, Disposition>;
  onReceipt: (opened: Receipt) => void;
  onInspected: (lineNo: number, result: InspectionResult) => void;
  onOverride: (dispositionId: string, next: Disposition) => void;
}) {
  return (
    <>
      <PageHeader title={rma.rmaId} subtitle="returns-processing · return authorization (RMA)" />
      <Card title="Return">
        <Facts
          items={[
            { label: "Status", value: <ReturnStatusPill status={rma.status} /> },
            { label: "Order", value: `${rma.orderId} · line ${rma.lineNo}` },
            { label: "SKU", value: rma.sku },
            { label: "Quantity", value: String(rma.quantity) },
            { label: "Site", value: rma.siteId },
            ...(rma.reasonCode ? [{ label: "Reason", value: rma.reasonCode }] : []),
            ...(rma.rejectionCode ? [{ label: "Rejection", value: rma.rejectionCode }] : []),
            ...(rma.windowEndsAt ? [{ label: "Window ends", value: utcLabel(rma.windowEndsAt) }] : []),
            ...(rma.authorizedAt ? [{ label: "Authorized", value: utcLabel(rma.authorizedAt) }] : []),
            ...(rma.rejectedAt ? [{ label: "Rejected", value: utcLabel(rma.rejectedAt) }] : []),
            ...(rma.receivedAt ? [{ label: "Received", value: utcLabel(rma.receivedAt) }] : []),
            ...(rma.closedAt ? [{ label: "Closed", value: utcLabel(rma.closedAt) }] : []),
            { label: "Version", value: String(rma.version) },
          ]}
        />
      </Card>
      {rma.status === "AUTHORIZED" && <ReceiveCard rma={rma} onReceipt={onReceipt} />}
      {receipt && (
        <ReceiptPanel
          receipt={receipt}
          inspections={inspections}
          overrides={overrides}
          onInspected={onInspected}
          onOverride={onOverride}
        />
      )}
    </>
  );
}

/** One editable line of the parcel being received. */
interface LineDraft {
  lineNo: string;
  sku: string;
  quantity: string;
  kind: LineKind;
  rmaId: string;
}

function emptyLine(rmaId: string): LineDraft {
  return { lineNo: "", sku: "", quantity: "", kind: "MATCHED", rmaId };
}

/** Open the receipt: POST /returns/{rmaId}/receipts. Every line is MATCHED
 *  (carrying the RMA it matches, prefilled with this RMA -- several RMAs may
 *  ship in one parcel) or explicitly UNEXPECTED. */
function ReceiveCard({ rma, onReceipt }: { rma: Return; onReceipt: (opened: Receipt) => void }) {
  const [siteId, setSiteId] = useState(rma.siteId);
  const [lines, setLines] = useState<LineDraft[]>([emptyLine(rma.rmaId)]);
  const [shown, setShown] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [saving, setSaving] = useState(false);

  const set = (i: number, patch: Partial<LineDraft>) =>
    setLines((prev) => prev.map((l, idx) => (idx === i ? { ...l, ...patch } : l)));

  const submit = async () => {
    setProblem(null);
    const siteProblem = idProblem("site id", siteId.trim());
    if (siteProblem !== null) {
      setShown(siteProblem);
      return;
    }
    const body = [];
    for (const l of lines) {
      const lineNo = positiveWhole(l.lineNo);
      const qty = positiveWhole(l.quantity);
      const message =
        (lineNo === null ? "Every line needs a whole line number, 1 or more." : null) ??
        idProblem("SKU", l.sku.trim()) ??
        (qty === null ? "Every line needs a whole quantity, 1 or more." : null) ??
        (l.kind === "MATCHED" && idProblem("RMA id", l.rmaId.trim()) !== null
          ? "A MATCHED line must carry the RMA it matches (or mark it UNEXPECTED)."
          : null);
      if (message !== null || lineNo === null || qty === null) {
        setShown(message);
        return;
      }
      body.push({
        lineNo,
        sku: l.sku.trim(),
        quantity: qty,
        kind: l.kind,
        ...(l.kind === "MATCHED" ? { rmaId: l.rmaId.trim() } : {}),
      });
    }
    setShown(null);
    setSaving(true);
    try {
      const opened = await openReturnReceipt(rma.rmaId, { siteId: siteId.trim(), lines: body });
      onReceipt(opened);
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title="Open the return receipt">
      <Form label="Open the return receipt" onSubmit={() => void submit()}>
        <FormRow>
          <TextField label="Site" value={siteId} onChange={setSiteId} required />
          <SubmitButton
            type="button"
            secondary
            onClick={() => setLines((prev) => [...prev, emptyLine(rma.rmaId)])}
          >
            Add line
          </SubmitButton>
        </FormRow>
        {lines.map((l, i) => (
          <FormRow key={i}>
            <TextField label="Line" value={l.lineNo} onChange={(v) => set(i, { lineNo: v })} inputMode="numeric" placeholder="1" required />
            <TextField label="SKU" value={l.sku} onChange={(v) => set(i, { sku: v })} placeholder="SKU-1" required />
            <TextField label="Quantity" value={l.quantity} onChange={(v) => set(i, { quantity: v })} inputMode="numeric" placeholder="1" required />
            <SelectField
              label="Kind"
              value={l.kind}
              onChange={(v) => set(i, { kind: v as LineKind })}
              options={[
                { value: "MATCHED", label: "MATCHED (an RMA)" },
                { value: "UNEXPECTED", label: "UNEXPECTED (no RMA)" },
              ]}
            />
            {l.kind === "MATCHED" && (
              <TextField label="RMA" value={l.rmaId} onChange={(v) => set(i, { rmaId: v })} required />
            )}
            {lines.length > 1 && (
              <SubmitButton type="button" secondary onClick={() => setLines((prev) => prev.filter((_, idx) => idx !== i))}>
                Remove
              </SubmitButton>
            )}
          </FormRow>
        ))}
        <InlineError message={shown} />
        {problem && (
          <ProblemAlert
            error={problem}
            lead={
              problem.slug === "rma-not-receivable"
                ? "This RMA can no longer receive goods (only an AUTHORIZED one can). Reload to see its latest state."
                : undefined
            }
          />
        )}
        <div>
          <SubmitButton disabled={saving}>Open receipt</SubmitButton>
        </div>
      </Form>
    </Card>
  );
}

/** The opened receipt and its lines: grading a line decides its disposition
 *  in the same transaction; an operator override supersedes it. */
function ReceiptPanel({
  receipt,
  inspections,
  overrides,
  onInspected,
  onOverride,
}: {
  receipt: Receipt;
  inspections: Record<number, InspectionResult>;
  overrides: Record<string, Disposition>;
  onInspected: (lineNo: number, result: InspectionResult) => void;
  onOverride: (dispositionId: string, next: Disposition) => void;
}) {
  return (
    <Card title={`Receipt ${receipt.receiptId}`}>
      <Stack>
        <Facts
          items={[
            { label: "Opened", value: utcLabel(receipt.openedAt) },
            { label: "Site", value: receipt.siteId },
            ...(receipt.rmaId ? [{ label: "Opened against", value: receipt.rmaId }] : []),
            { label: "Lines", value: String(receipt.lines.length) },
          ]}
        />
        {receipt.lines.map((line) => {
          const inspection = inspections[line.lineNo];
          const effective = inspection
            ? (overrides[inspection.disposition.dispositionId] ?? inspection.disposition)
            : null;
          return (
            <Card key={line.lineNo} title={`Line ${line.lineNo} — ${line.sku}`}>
              <Stack>
                <Facts
                  items={[
                    { label: "Received quantity", value: String(line.quantity) },
                    { label: "Kind", value: line.kind === "MATCHED" ? `MATCHED (${line.rmaId})` : "UNEXPECTED" },
                    ...(inspection
                      ? [
                          { label: "Grade", value: <GradePill grade={inspection.grade} /> },
                          { label: "Reasons", value: inspection.reasonCodes.join(", ") || "—" },
                          { label: "Inspected", value: utcLabel(inspection.inspectedAt) },
                        ]
                      : [{ label: "Inspected", value: line.inspected ? "yes" : "not yet" }]),
                  ]}
                />
                {!inspection && !line.inspected && (
                  <InspectForm receipt={receipt} line={line} onInspected={onInspected} />
                )}
                {inspection && effective && (
                  <DispositionFacts disposition={effective} overridden={effective.dispositionId !== inspection.disposition.dispositionId} />
                )}
                {inspection && effective && (
                  <OverrideForm disposition={effective} onOverride={onOverride} />
                )}
              </Stack>
            </Card>
          );
        })}
      </Stack>
    </Card>
  );
}

/** Grade one line: POST /receipts/{id}/lines/{n}/inspect. The quantity is
 *  optional (empty = the full received quantity). */
function InspectForm({
  receipt,
  line,
  onInspected,
}: {
  receipt: Receipt;
  line: { lineNo: number; sku: string; quantity: number };
  onInspected: (lineNo: number, result: InspectionResult) => void;
}) {
  const [grade, setGrade] = useState("");
  const [quantity, setQuantity] = useState("");
  const [reasons, setReasons] = useState("");
  const [shown, setShown] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setProblem(null);
    const qty = quantity.trim() === "" ? undefined : positiveWhole(quantity);
    const codes = reasonCodes(reasons);
    const message =
      (grade === "" ? "Pick a grade (A, B, C or SCRAP)." : null) ??
      (quantity.trim() !== "" && qty === null ? "Quantity must be a whole number, 1 or more." : null) ??
      (codes === null ? "Give at least one reason code (e.g. CUSTOMER_DAMAGE)." : null);
    setShown(message);
    if (message !== null || qty === null || codes === null) return;
    setSaving(true);
    try {
      const result = await inspectReturnLine(receipt.receiptId, line.lineNo, {
        grade: grade as InspectionResult["grade"],
        ...(qty === undefined ? {} : { quantity: qty }),
        reasonCodes: codes,
      });
      onInspected(line.lineNo, result);
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Form label={`Inspect line ${line.lineNo}`} onSubmit={() => void submit()}>
      <FormRow>
        <SelectField
          label="Grade"
          value={grade}
          onChange={setGrade}
          options={GRADES.map((g) => ({ value: g, label: g }))}
          required
        />
        <TextField
          label="Quantity"
          value={quantity}
          onChange={setQuantity}
          inputMode="numeric"
          placeholder={String(line.quantity)}
          hint="Optional; the full received quantity when empty"
        />
        <TextField
          label="Reasons"
          value={reasons}
          onChange={setReasons}
          placeholder="CUSTOMER_DAMAGE"
          required
          hint="Comma-separated reason codes"
        />
        <SubmitButton disabled={saving}>Inspect line</SubmitButton>
      </FormRow>
      <InlineError message={shown} />
      {problem && (
        <ProblemAlert
          error={problem}
          lead={
            problem.slug === "line-already-inspected"
              ? "This line already has a grade. A return line is inspected exactly once."
              : problem.slug === "inspected-exceeds-received"
                ? "The inspected quantity must not exceed the received quantity of the line."
                : undefined
          }
        />
      )}
    </Form>
  );
}

function DispositionFacts({ disposition, overridden }: { disposition: Disposition; overridden: boolean }) {
  return (
    <Facts
      items={[
        { label: overridden ? "Override outcome" : "Disposition", value: <OutcomePill outcome={disposition.outcome} /> },
        { label: "Id", value: disposition.dispositionId },
        { label: "Policy version", value: String(disposition.policyVersion) },
        { label: "Staged", value: disposition.staged ? "yes (ReturnLineStaged raised)" : "no" },
        ...(disposition.supersedes ? [{ label: "Supersedes", value: disposition.supersedes }] : []),
        ...(disposition.reason ? [{ label: "Reason", value: disposition.reason }] : []),
        { label: "Decided", value: utcLabel(disposition.decidedAt) },
      ]}
    />
  );
}

/** Override the decided disposition: POST /dispositions/{id}/override with a
 *  mandatory reason. The original decision is never mutated -- the override
 *  supersedes it. */
function OverrideForm({
  disposition,
  onOverride,
}: {
  disposition: Disposition;
  onOverride: (dispositionId: string, next: Disposition) => void;
}) {
  const [outcome, setOutcome] = useState("");
  const [reason, setReason] = useState("");
  const [shown, setShown] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setProblem(null);
    setSaved(false);
    const message =
      (outcome === "" ? "Pick the outcome the operator decided." : null) ??
      (reason.trim().length === 0 ? "An override must say why (a reason is mandatory)." : null);
    setShown(message);
    if (message !== null) return;
    setSaving(true);
    try {
      const next = await overrideDisposition(disposition.dispositionId, {
        outcome: outcome as Disposition["outcome"],
        reason: reason.trim(),
      });
      setSaved(true);
      onOverride(disposition.dispositionId, next);
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Form label={`Override ${disposition.dispositionId}`} onSubmit={() => void submit()}>
      <FormRow>
        <SelectField
          label="Outcome"
          value={outcome}
          onChange={setOutcome}
          options={OUTCOMES.map((o) => ({ value: o, label: o }))}
          required
        />
        <TextField label="Reason" value={reason} onChange={setReason} placeholder="vendor accepts open-box returns this quarter" required />
        <SubmitButton secondary disabled={saving}>
          Override disposition
        </SubmitButton>
      </FormRow>
      <InlineError message={shown} />
      {saved && <InlineSuccess message="Override recorded; it supersedes the decision above." />}
      {problem && <ProblemAlert error={problem} />}
    </Form>
  );
}
