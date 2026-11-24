// Copyright (c) 2026 Reliant Labs

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ChatModelCallsTable, callCost } from "../ChatModelCallsTable";
import { ownerTrace, viewerTrace } from "./fixtures";

describe("ChatModelCallsTable", () => {
  it("lists each model call with its cost and basis for the owner", () => {
    render(<ChatModelCallsTable calls={ownerTrace().modelCalls} viewer={false} />);
    const table = screen.getByRole("table", { name: "Model calls" });
    expect(within(table).getByText("Cost")).toBeInTheDocument();
    expect(screen.getAllByTestId("chat-trace-call-cost").map((cell) => cell.textContent)).toEqual([
      "$0.0123",
      "~$0.0020",
      "Unpriced",
    ]);
    expect(within(table).getByText("claude-sonnet-5")).toBeInTheDocument();
    expect(within(table).getByText("Failed (rate limit)")).toBeInTheDocument();
    expect(within(table).getByText("Title")).toBeInTheDocument();
  });

  it("gives a viewer no cost column", () => {
    const { container } = render(<ChatModelCallsTable calls={viewerTrace().modelCalls} viewer />);
    expect(screen.queryByText("Cost")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-trace-call-cost")).not.toBeInTheDocument();
    expect(container.textContent).not.toMatch(/\$/);
    expect(screen.getByText("gpt-5-mini")).toBeInTheDocument();
  });

  it("says a window with no calls is empty", () => {
    render(<ChatModelCallsTable calls={[]} viewer={false} />);
    expect(screen.getByText("No model calls in this window.")).toBeInTheDocument();
  });

  it("reads a call with no recorded cost as unknown, never zero", () => {
    const [call] = ownerTrace().modelCalls;
    expect(callCost({ ...call, costUsdNanos: undefined, costBasis: "" })).toBe("—");
  });
});
