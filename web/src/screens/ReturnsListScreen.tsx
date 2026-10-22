import { useState } from "react";
import { Link } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listReturns } from "../api";
import { Form, FormRow, InlineError, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, ReturnStatusPill, Stack } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { idProblem, utcLabel } from "../lib";
import type { Return } from "../types";

/**
 * Returns list: GET /returns?orderRef= -- the RMAs of ONE order, newest
 * first. An unknown order answers an empty list, never an error. "Authorize
 * a return" opens the authorization form. (The API deliberately serves no
 * cross-order listing: the reverse flow starts from an order.)
 */
export function ReturnsListScreen() {
  const [draft, setDraft] = useState("");
  const [applied, setApplied] = useState("");
  const [shown, setShown] = useState<string | null>(null);
  const page = useRequest(applied === "" ? null : () => listReturns(applied), `returns|${applied}`);

  const problem = idProblem("order id", draft.trim());
  const apply = () => {
    if (problem !== null) {
      setShown(problem);
      return;
    }
    setShown(null);
    setApplied(draft.trim());
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Returns"
        subtitle="returns-processing · return authorizations (RMAs) against a shipped order line"
      />
      <Card title="Find by order">
        <Form label="Return filters" onSubmit={apply}>
          <FormRow>
            <TextField
              label="Order"
              value={draft}
              onChange={setDraft}
              placeholder="ord-7c9e2f1a"
              required
              hint="Every RMA of this order, newest first"
            />
            <SubmitButton>Find returns</SubmitButton>
          </FormRow>
          <InlineError message={shown} />
        </Form>
      </Card>
      <Card
        title="RMAs"
        actions={
          <Link to="../returns/new">Authorize a return</Link>
        }
      >
        <RequestView
          state={page}
          what="returns"
          whenIdle="Enter an order id to list its return authorizations."
          isEmpty={(items) => items.length === 0}
          empty="No RMAs for this order (an order with no returns answers with an empty list)."
        >
          {(items) => <ReturnTable returns={items} />}
        </RequestView>
      </Card>
    </Stack>
  );
}

function ReturnTable({ returns }: { returns: Return[] }) {
  return (
    <DataTable
      rowKey={(r) => r.rmaId}
      rows={returns}
      columns={[
        {
          key: "rma",
          header: "RMA",
          render: (r) => <Link to={`../returns/${encodeURIComponent(r.rmaId)}`}>{r.rmaId}</Link>,
        },
        { key: "line", header: "Order line", align: "right", render: (r) => String(r.lineNo) },
        { key: "sku", header: "SKU", render: (r) => r.sku },
        { key: "qty", header: "Quantity", align: "right", render: (r) => String(r.quantity) },
        { key: "site", header: "Site", render: (r) => r.siteId },
        { key: "status", header: "Status", render: (r) => <ReturnStatusPill status={r.status} /> },
        {
          key: "reason",
          header: "Reason / rejection",
          render: (r) => r.reasonCode ?? r.rejectionCode ?? "",
        },
        { key: "window", header: "Window ends", render: (r) => utcLabel(r.windowEndsAt) },
        { key: "authorized", header: "Authorized", render: (r) => utcLabel(r.authorizedAt) },
      ]}
    />
  );
}
