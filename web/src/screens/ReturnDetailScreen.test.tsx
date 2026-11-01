import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { ReturnDetailRoute } from "./ReturnDetailScreen";
import { json, mockApi } from "../test/fetchMock";
import { DISPOSITION_ID, RECEIPT_ID, RMA_ID, disposition, inspection, receipt, rma } from "../test/fixtures";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const RTV_DISPOSITION = disposition({
  dispositionId: "disp-override-1",
  outcome: "RTV",
  supersedes: DISPOSITION_ID,
  staged: false,
  reason: "vendor accepts open-box returns this quarter",
});

/** GET /returns/{id}: AUTHORIZED until the receipt is opened, RECEIVED after
 *  (the POST's effect), exactly like the service. */
function mountDetail() {
  let served = 0;
  const api = mockApi({
    [`GET /returns/${RMA_ID}`]: () =>
      json(rma(served++ === 0 ? {} : { status: "RECEIVED", receivedAt: "2026-10-21T10:00:00Z", version: 2 })),
    [`POST /returns/${RMA_ID}/receipts`]: json(receipt(), 201),
    [`POST /receipts/${RECEIPT_ID}/lines/1/inspections`]: json(inspection()),
    [`POST /dispositions/${DISPOSITION_ID}/overrides`]: json(RTV_DISPOSITION),
  });
  const view = render(
    <MemoryRouter initialEntries={[`/returns/${RMA_ID}`]}>
      <Routes>
        <Route path="/">
          <Route path="returns/:rmaId" element={<ReturnDetailRoute />} />
          <Route path="returns" element={<p>all returns</p>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
  return { api, view };
}

describe("ReturnDetailScreen", () => {
  it("renders the RMA and, while AUTHORIZED, the receive form", async () => {
    mountDetail();
    expect(await screen.findByRole("heading", { name: RMA_ID })).toBeInTheDocument();
    expect(screen.getByText("Open the return receipt")).toBeInTheDocument();
    expect(screen.getByText("DAMAGED")).toBeInTheDocument();
    expect(screen.getByText("2026-11-20 09:30Z")).toBeInTheDocument();
  });

  it("opens the receipt with an Idempotency-Key, then shows its lines", async () => {
    const { api } = mountDetail();
    await screen.findByRole("heading", { name: RMA_ID });
    await userEvent.type(screen.getByLabelText(/^Line \*/), "1");
    await userEvent.type(screen.getByLabelText(/^SKU/), "SKU-1");
    await userEvent.type(screen.getByLabelText(/^Quantity/), "2");
    await userEvent.click(screen.getByRole("button", { name: "Open receipt" }));
    expect(await screen.findByText("Line 1 — SKU-1")).toBeInTheDocument();
    const [post] = api.to(`POST /returns/${RMA_ID}/receipts`);
    expect(post.body).toEqual({
      siteId: "WH1",
      lines: [{ lineNo: 1, sku: "SKU-1", quantity: 2, kind: "MATCHED", rmaId: RMA_ID }],
    });
    expect(post.headers["Idempotency-Key"]).toMatch(UUID);
    // The RMA moved to RECEIVED, so the receive form is gone.
    await screen.findByText("RECEIVED");
    expect(screen.queryByText("Open the return receipt")).toBeNull();
  });

  it("receives an UNEXPECTED line without an rmaId", async () => {
    const { api } = mountDetail();
    await screen.findByRole("heading", { name: RMA_ID });
    await userEvent.type(screen.getByLabelText(/^Line \*/), "1");
    await userEvent.type(screen.getByLabelText(/^SKU/), "SKU-9");
    await userEvent.type(screen.getByLabelText(/^Quantity/), "1");
    await userEvent.selectOptions(screen.getByLabelText(/^Kind/), "UNEXPECTED (no RMA)");
    await userEvent.click(screen.getByRole("button", { name: "Open receipt" }));
    await screen.findByText(`Receipt ${RECEIPT_ID}`);
    expect(api.to(`POST /returns/${RMA_ID}/receipts`)[0].body).toEqual({
      siteId: "WH1",
      lines: [{ lineNo: 1, sku: "SKU-9", quantity: 1, kind: "UNEXPECTED" }],
    });
  });

  it("inspects a line (Idempotency-Key, quantity defaults) and shows the decided disposition, then overrides it", async () => {
    const { api } = mountDetail();
    await screen.findByRole("heading", { name: RMA_ID });
    await userEvent.type(screen.getByLabelText(/^Line \*/), "1");
    await userEvent.type(screen.getByLabelText(/^SKU/), "SKU-1");
    await userEvent.type(screen.getByLabelText(/^Quantity/), "2");
    await userEvent.click(screen.getByRole("button", { name: "Open receipt" }));
    await screen.findByText("Line 1 — SKU-1");
    await userEvent.selectOptions(screen.getByLabelText(/^Grade/), "B");
    await userEvent.type(screen.getByLabelText(/^Reasons/), "CUSTOMER_DAMAGE, OPENED_BOX");
    await userEvent.click(screen.getByRole("button", { name: "Inspect line" }));
    // The policy decision, from the inspect response's disposition.
    expect(await screen.findByText(/yes \(ReturnLineStaged raised\)/)).toBeInTheDocument();
    const [inspect] = api.to(`POST /receipts/${RECEIPT_ID}/lines/1/inspections`);
    expect(inspect.body).toEqual({ grade: "B", reasonCodes: ["CUSTOMER_DAMAGE", "OPENED_BOX"] });
    expect(inspect.headers["Idempotency-Key"]).toMatch(UUID);
    // Override it with a reason; the override supersedes the decision.
    await userEvent.selectOptions(screen.getByLabelText(/^Outcome/), "RTV");
    await userEvent.type(screen.getByLabelText(/^Reason \*/), "vendor accepts open-box returns this quarter");
    await userEvent.click(screen.getByRole("button", { name: "Override disposition" }));
    expect(await screen.findByText("Override recorded; it supersedes the decision above.")).toBeInTheDocument();
    const [override] = api.to(`POST /dispositions/${DISPOSITION_ID}/overrides`);
    expect(override.body).toEqual({ outcome: "RTV", reason: "vendor accepts open-box returns this quarter" });
    expect(override.headers["Idempotency-Key"]).toMatch(UUID);
    expect(screen.getByText("disp-override-1")).toBeInTheDocument();
  });

  it("renders the problem of a failed receive", async () => {
    const api = mockApi({
      [`GET /returns/${RMA_ID}`]: json(rma()),
      [`POST /returns/${RMA_ID}/receipts`]: new Response(
        JSON.stringify({
          type: "https://errors.returns-processing.warehouse-systems.dev/rma-not-receivable",
          title: "The RMA cannot receive goods",
          status: 409,
          detail: "only an AUTHORIZED RMA can be received",
          instance: "/x",
        }),
        { status: 409, headers: { "Content-Type": "application/problem+json" } },
      ),
    });
    render(
      <MemoryRouter initialEntries={[`/returns/${RMA_ID}`]}>
        <Routes>
          <Route path="/returns/:rmaId" element={<ReturnDetailRoute />} />
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByRole("heading", { name: RMA_ID });
    await userEvent.type(screen.getByLabelText(/^Line \*/), "1");
    await userEvent.type(screen.getByLabelText(/^SKU/), "SKU-1");
    await userEvent.type(screen.getByLabelText(/^Quantity/), "2");
    await userEvent.click(screen.getByRole("button", { name: "Open receipt" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("only an AUTHORIZED RMA can be received");
    expect(alert).toHaveTextContent(/Reload to see its latest state/);
    expect(api.calls).toHaveLength(2);
  });
});
