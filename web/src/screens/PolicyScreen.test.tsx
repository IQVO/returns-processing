import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { PolicyScreen } from "./PolicyScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { policy } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter initialEntries={["/policy"]}>
      <PolicyScreen />
    </MemoryRouter>,
  );
}

describe("PolicyScreen", () => {
  it("renders the current policy: bounds, veto fallback and the rule table", async () => {
    mockApi({ "GET /disposition-policies/current": json(policy()) });
    mount();
    expect(await screen.findByRole("heading", { name: "Disposition policy" })).toBeInTheDocument();
    expect(screen.getByText("30 days after authorization")).toBeInTheDocument();
    expect(screen.getByText("10")).toBeInTheDocument();
    expect(screen.getByText("Hazmat")).toBeInTheDocument();
    expect(screen.getAllByText("* (any — the grade default)")).toHaveLength(4);
    expect(screen.getByText(/append-only/)).toBeInTheDocument();
  });

  it("renders the never-declared state as an empty screen, not an error", async () => {
    mockApi({ "GET /disposition-policies/current": problem(404, "policy-not-found", "No disposition policy declared", "no disposition policy version exists yet") });
    mount();
    expect(await screen.findByText(/No disposition policy version exists yet/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("degrades to the problem alert when the API is unreachable", async () => {
    mockApi({});
    mount();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });
});
