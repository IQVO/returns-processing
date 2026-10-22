import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import App from "./App";
import { json, mockApi } from "./test/fetchMock";
import { RMA_ID, policy, rma } from "./test/fixtures";

function mount(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

/** Mounted the way the console mounts the remote: inside the host's own
 *  splat route, where relative links resolve differently than at the root. */
function mountUnderHost(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/returns-processing/*" element={<App />} />
      </Routes>
    </MemoryRouter>,
  );
}

function routes() {
  return mockApi({
    "GET /returns": json({ items: [rma()] }),
    [`GET /returns/${RMA_ID}`]: json(rma()),
    "GET /disposition-policies/current": json(policy()),
  });
}

function subNavHrefs(): string[] {
  const nav = screen.getByRole("navigation", { name: "Returns processing sections" });
  return within(nav)
    .getAllByRole("link")
    .map((a) => a.getAttribute("href") ?? "");
}

describe("App (the exposed ./App)", () => {
  it("redirects the index route to the returns list and shows the sub-nav", async () => {
    routes();
    mount("/");
    expect(await screen.findByRole("heading", { name: "Returns" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Returns processing sections" });
    for (const label of ["Returns", "Disposition policy"]) expect(nav).toHaveTextContent(label);
  });

  it("routes relatively at the root: every section and detail route renders", async () => {
    routes();
    const cases: [string, string][] = [
      ["/returns", "Returns"],
      ["/returns/new", "Authorize a return"],
      [`/returns/${RMA_ID}`, RMA_ID],
      ["/policy", "Disposition policy"],
    ];
    for (const [path, heading] of cases) {
      const { unmount } = mount(path);
      expect(await screen.findByRole("heading", { name: heading })).toBeInTheDocument();
      unmount();
    }
  });

  it("degrades without crashing when the API is unreachable", async () => {
    mockApi({});
    mount("/policy");
    expect(await screen.findByRole("heading", { name: "Disposition policy" })).toBeInTheDocument();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  it("keeps sub-nav links under the host's mount point from any section", async () => {
    routes();
    mountUnderHost("/returns-processing/policy");
    await screen.findByRole("heading", { name: "Disposition policy" });
    expect(subNavHrefs()).toEqual(["/returns-processing/returns", "/returns-processing/policy"]);
    await userEvent.click(screen.getByRole("link", { name: "Returns" }));
    expect(await screen.findByRole("heading", { name: "Returns" })).toBeInTheDocument();
    expect(subNavHrefs()[0]).toBe("/returns-processing/returns");
  });

  it("only ever calls its own API (no sibling base)", async () => {
    const api = routes();
    mount("/returns");
    await userEvent.type(screen.getByLabelText(/^Order/), "ord-7c9e2f1a");
    await userEvent.click(screen.getByRole("button", { name: "Find returns" }));
    await screen.findByText(RMA_ID);
    for (const c of api.calls) expect(c.url.startsWith("http://localhost:8080/")).toBe(true);
  });
});
