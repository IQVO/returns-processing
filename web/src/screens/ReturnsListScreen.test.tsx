import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { ReturnsListScreen } from "./ReturnsListScreen";
import { json, mockApi } from "../test/fetchMock";
import { RMA_ID, rma } from "../test/fixtures";

function mount(path = "/returns") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/">
          <Route path="returns" element={<ReturnsListScreen />} />
          <Route path="returns/:rmaId" element={<p>landed on an RMA</p>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("ReturnsListScreen", () => {
  it("asks for an order before calling anything", async () => {
    const api = mockApi({});
    mount();
    expect(screen.getByText("Enter an order id to list its return authorizations.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Find returns" }));
    expect(screen.getByText("An order id is required.")).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });

  it("lists the RMAs of the order and links each to its detail", async () => {
    mockApi({ "GET /returns": json({ items: [rma(), rma({ rmaId: "rma-other", lineNo: 2, status: "REJECTED", reasonCode: undefined, rejectionCode: "LINE_NOT_MANIFESTED" })] }) });
    mount();
    await userEvent.type(screen.getByLabelText(/^Order/), "ord-7c9e2f1a");
    await userEvent.click(screen.getByRole("button", { name: "Find returns" }));
    const first = await screen.findByRole("link", { name: RMA_ID });
    expect(first).toHaveAttribute("href", "/returns/" + RMA_ID);
    expect(await screen.findByText("rma-other")).toBeInTheDocument();
    expect(screen.getByText("LINE_NOT_MANIFESTED")).toBeInTheDocument();
    expect(screen.getByText("DAMAGED")).toBeInTheDocument();
  });

  it("shows the empty state for an order with no returns (never an error)", async () => {
    mockApi({ "GET /returns": json({ items: [] }) });
    mount();
    await userEvent.type(screen.getByLabelText(/^Order/), "ord-none");
    await userEvent.click(screen.getByRole("button", { name: "Find returns" }));
    expect(await screen.findByText(/No RMAs for this order/)).toBeInTheDocument();
  });
});
