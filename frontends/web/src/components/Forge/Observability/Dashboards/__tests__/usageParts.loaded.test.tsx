// Copyright (c) 2026 Reliant Labs
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { Loaded, type LoadedQuery } from "../usageParts";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

type Answer = { orgId: string; figure: string };

function query(over: Partial<LoadedQuery<Answer>>): LoadedQuery<Answer> {
  return { isPending: false, isError: false, isFetching: false, data: undefined, refetch: vi.fn(), ...over };
}

function panel(q: LoadedQuery<Answer>, orgId = "org-a") {
  return (
    <Loaded query={q} orgId={orgId} what="credit">
      {(a) => <span>{a.figure}</span>}
    </Loaded>
  );
}

describe("Loaded", () => {
  it("shows the answer for the organization asked about", () => {
    render(panel(query({ data: { orgId: "org-a", figure: "$4.00" } })));
    expect(screen.getByText("$4.00")).toBeInTheDocument();
  });

  it("hides foreign cached data without starting, repeating, or cancelling a query fetch", () => {
    const q = query({ isFetching: true, data: { orgId: "org-b", figure: "$9.99" } });
    const { rerender } = render(panel(q));
    expect(screen.getByRole("status")).toHaveTextContent("Loading credit…");
    expect(screen.queryByText("$9.99")).not.toBeInTheDocument();
    rerender(panel({ ...q, data: undefined }));
    expect(screen.getByRole("status")).toHaveTextContent("Loading credit…");
    rerender(panel({ ...q, data: { orgId: "org-c", figure: "$6.66" } }));
    expect(screen.queryByText("$6.66")).not.toBeInTheDocument();
    rerender(panel({ ...q, isFetching: false }));
    expect(screen.getByTestId("panel-unavailable")).toHaveTextContent("Couldn't load credit.");
    expect(screen.queryByText("$9.99")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/organization/i);
    expect(q.refetch).not.toHaveBeenCalled();
  });

  it("says plainly that a failed read could not load, with a Try again that refetches", async () => {
    const user = userEvent.setup();
    const q = query({ isError: true });
    render(panel(q));
    const unavailable = screen.getByTestId("panel-unavailable");
    expect(unavailable).toHaveTextContent("Couldn't load credit.");
    expect(unavailable).not.toHaveTextContent(/unknown|not zero/);
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(q.refetch).toHaveBeenCalledTimes(1);
  });
});
