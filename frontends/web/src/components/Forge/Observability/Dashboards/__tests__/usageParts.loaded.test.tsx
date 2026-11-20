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

  it("treats another organization's answer as still loading, asks again once, and says nothing about it", () => {
    const q = query({ data: { orgId: "org-b", figure: "$9.99" } });
    const { rerender } = render(panel(q));
    expect(screen.getByRole("status")).toHaveTextContent("Loading credit…");
    expect(screen.queryByText("$9.99")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/organization/i);
    expect(q.refetch).toHaveBeenCalledTimes(1);
    // Re-renders with the same foreign answer do not ask again.
    rerender(panel({ ...q, isFetching: true }));
    rerender(panel(q));
    expect(q.refetch).toHaveBeenCalledTimes(1);
    // Asked again and still foreign: a plain "couldn't load", never the figure.
    expect(screen.getByTestId("panel-unavailable")).toHaveTextContent("Couldn't load credit.");
    expect(screen.queryByText("$9.99")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/organization/i);
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
