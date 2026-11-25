// Copyright (c) 2026 Reliant Labs

/**
 * The owner reads reported and estimated cost apart, with the unpriced share;
 * a viewer reads calls, tokens and time, and no dollar figure anywhere.
 */

import { create } from "@bufbuild/protobuf";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ChatTraceTotalsSchema } from "@/gen/reliant/v1/chat_trace_pb";

import { ChatTraceCostSummary } from "../ChatTraceCostSummary";
import { ownerTrace, viewerTrace } from "./fixtures";

describe("ChatTraceCostSummary", () => {
  it("shows the owner reported and estimated cost separately, and the unpriced share", () => {
    render(<ChatTraceCostSummary totals={ownerTrace().totals} viewer={false} />);
    expect(screen.getByTestId("chat-trace-calls")).toHaveTextContent("3");
    expect(screen.getByTestId("chat-trace-calls")).toHaveTextContent("1 failed call");
    expect(screen.getByTestId("chat-trace-cost-reported")).toHaveTextContent("$0.0123");
    expect(screen.getByTestId("chat-trace-cost-reported")).toHaveTextContent("charged for 1 call");
    expect(screen.getByTestId("chat-trace-cost-estimated")).toHaveTextContent("~$0.0020");
    expect(screen.getByTestId("chat-trace-cost-estimated")).toHaveTextContent("list price for 1 call");
    expect(screen.getByTestId("chat-trace-cost-unpriced")).toHaveTextContent("33%");
    expect(screen.getByTestId("chat-trace-cost-unpriced")).toHaveTextContent("1 of 3 calls: 1 call on a subscription or local model");
  });

  it("never reads unknown cost as zero", () => {
    const totals = create(ChatTraceTotalsSchema, { calls: 2n, unknownCostCalls: 2n });
    render(<ChatTraceCostSummary totals={totals} viewer={false} />);
    expect(screen.getByTestId("chat-trace-cost-reported")).toHaveTextContent("—");
    expect(screen.getByTestId("chat-trace-cost-estimated")).toHaveTextContent("—");
    expect(screen.getByTestId("chat-trace-cost-unpriced")).toHaveTextContent("100%");
    expect(screen.getByTestId("chat-trace-cost-unpriced")).toHaveTextContent("2 calls with no recorded cost");
    expect(screen.queryByText(/\$0\.00/)).not.toBeInTheDocument();
  });

  it("gives a viewer calls, tokens and time, and no dollars", () => {
    const { container } = render(<ChatTraceCostSummary totals={viewerTrace().totals} viewer />);
    expect(screen.getByTestId("chat-trace-calls")).toHaveTextContent("3");
    expect(screen.getByText("Input tokens")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-trace-cost")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-trace-viewer-note")).toBeInTheDocument();
    expect(container.textContent).not.toMatch(/\$/);
  });

  it("hides dollars from a viewer even if the totals carried them", () => {
    const { container } = render(<ChatTraceCostSummary totals={ownerTrace().totals} viewer />);
    expect(container.textContent).not.toMatch(/\$/);
  });
});
