import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthorizeReturnScreen } from "./AuthorizeReturnScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { rma } from "../test/fixtures";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

function mount(path = "/returns/new") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/">
          <Route path="returns/new" element={<AuthorizeReturnScreen />} />
          <Route path="returns/:rmaId" element={<p>landed on an RMA</p>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

async function fill() {
  await userEvent.type(screen.getByLabelText(/^Order \*/), "ord-7c9e2f1a");
  await userEvent.type(screen.getByLabelText(/Order line/), "1");
  await userEvent.type(screen.getByLabelText(/^SKU/), "SKU-1");
  await userEvent.type(screen.getByLabelText(/^Quantity/), "2");
  await userEvent.type(screen.getByLabelText(/^Site/), "WH1");
  await userEvent.type(screen.getByLabelText(/^Reason \*/), "DAMAGED");
}

describe("AuthorizeReturnScreen", () => {
  it("posts the authorization with an Idempotency-Key and opens the RMA", async () => {
    const api = mockApi({ "POST /returns": json(rma(), 201) });
    mount("/returns/new?orderId=ord-7c9e2f1a");
    expect(screen.getByLabelText(/^Order \*/)).toHaveValue("ord-7c9e2f1a");
    await userEvent.type(screen.getByLabelText(/Order line/), "1");
    await userEvent.type(screen.getByLabelText(/^SKU/), "SKU-1");
    await userEvent.type(screen.getByLabelText(/^Quantity/), "2");
    await userEvent.type(screen.getByLabelText(/^Site/), "WH1");
    await userEvent.type(screen.getByLabelText(/^Reason \*/), "DAMAGED");
    await userEvent.click(screen.getByRole("button", { name: "Authorize return" }));
    expect(await screen.findByText("landed on an RMA")).toBeInTheDocument();
    const [post] = api.to("POST /returns");
    expect(post.body).toEqual({ orderId: "ord-7c9e2f1a", lineNo: 1, sku: "SKU-1", quantity: 2, siteId: "WH1", reasonCode: "DAMAGED" });
    expect(post.headers["Idempotency-Key"]).toMatch(UUID);
  });

  it("validates every field without calling the API", async () => {
    const api = mockApi({});
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Authorize return" }));
    expect(screen.getByText("An order id is required.")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/^Order \*/), "ord-1");
    await userEvent.click(screen.getByRole("button", { name: "Authorize return" }));
    expect(screen.getByText("The order line must be a whole number, 1 or more.")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText(/Order line/), "0");
    await userEvent.click(screen.getByRole("button", { name: "Authorize return" }));
    expect(screen.getByText("The order line must be a whole number, 1 or more.")).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });

  it.each([
    ["return-not-authorizable", /never provably shipped/],
    ["open-rma-exists", /already open for this order line/],
  ])("explains a %s refusal", async (slug, lead) => {
    mockApi({ "POST /returns": problem(409, slug, "Refused", "server detail") });
    mount("/returns/new?orderId=ord-7c9e2f1a");
    await fill();
    await userEvent.click(screen.getByRole("button", { name: "Authorize return" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(lead);
    expect(alert).toHaveTextContent("server detail");
  });
});
