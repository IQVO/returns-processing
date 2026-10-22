import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import { authorizeReturn, toApiError } from "../api";
import type { ApiError } from "../api";
import { Form, FormRow, InlineError, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, Stack } from "../components/layout";
import { ProblemAlert } from "../components/ProblemAlert";
import { idProblem, positiveWhole } from "../lib";

/** What to tell the operator for each refusal of an authorization. */
const LEADS: Record<string, string> = {
  "return-not-authorizable":
    "The order line is not authorizable: it never provably shipped (no manifested work unit), the return window closed, or the quantity passes the policy cap.",
  "open-rma-exists": "An RMA is already open for this order line. Receive it (or let it close) before authorizing another.",
  "idempotency-key-reused": "This submission was already sent with a different body. Start a new one.",
};

/**
 * Authorize a return: POST /returns for one order line. The POST carries an
 * Idempotency-Key minted for this submit; on 201 the screen moves to the new
 * RMA. The order id (and, when present, line and SKU) can be prefilled from
 * the list screen's query (?orderId=&lineNo=&sku=).
 */
export function AuthorizeReturnScreen() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [orderId, setOrderId] = useState(params.get("orderId") ?? "");
  const [lineNo, setLineNo] = useState(params.get("lineNo") ?? "");
  const [sku, setSku] = useState(params.get("sku") ?? "");
  const [siteId, setSiteId] = useState("");
  const [quantity, setQuantity] = useState("");
  const [reasonCode, setReasonCode] = useState("");
  const [shown, setShown] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setProblem(null);
    const line = positiveWhole(lineNo);
    const qty = positiveWhole(quantity);
    const message =
      idProblem("order id", orderId.trim()) ??
      (line === null ? "The order line must be a whole number, 1 or more." : null) ??
      idProblem("SKU", sku.trim()) ??
      (qty === null ? "Quantity must be a whole number, 1 or more." : null) ??
      idProblem("site id", siteId.trim()) ??
      idProblem("return reason", reasonCode.trim());
    setShown(message);
    if (message !== null || line === null || qty === null) return;
    setSaving(true);
    try {
      const created = await authorizeReturn({
        orderId: orderId.trim(),
        lineNo: line,
        sku: sku.trim(),
        quantity: qty,
        siteId: siteId.trim(),
        reasonCode: reasonCode.trim(),
      });
      void navigate(`../returns/${encodeURIComponent(created.rmaId)}`);
    } catch (err) {
      setProblem(toApiError(err));
      setSaving(false);
    }
  };

  return (
    <Stack gap={5}>
      <div>
        <Link to="../returns">← All returns</Link>
      </div>
      <PageHeader
        title="Authorize a return"
        subtitle="returns-processing · open an RMA against an allocated, manifested order line"
      />
      <Card title="New RMA">
        <Form label="Authorize a return" onSubmit={() => void submit()}>
          <FormRow>
            <TextField label="Order" value={orderId} onChange={setOrderId} placeholder="ord-7c9e2f1a" required />
            <TextField label="Order line" value={lineNo} onChange={setLineNo} inputMode="numeric" placeholder="1" required />
            <TextField label="SKU" value={sku} onChange={setSku} placeholder="SKU-1" required />
            <TextField label="Quantity" value={quantity} onChange={setQuantity} inputMode="numeric" placeholder="1" required />
            <TextField label="Site" value={siteId} onChange={setSiteId} placeholder="WH1" required />
            <TextField label="Reason" value={reasonCode} onChange={setReasonCode} placeholder="DAMAGED" required hint="e.g. DAMAGED, WRONG_ITEM, NOT_NEEDED" />
          </FormRow>
          <InlineError message={shown} />
          {problem && <ProblemAlert error={problem} lead={LEADS[problem.slug]} />}
          <div>
            <SubmitButton disabled={saving}>Authorize return</SubmitButton>
          </div>
        </Form>
      </Card>
    </Stack>
  );
}
