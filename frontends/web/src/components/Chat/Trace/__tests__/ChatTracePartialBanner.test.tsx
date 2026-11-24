// Copyright (c) 2026 Reliant Labs

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ChatTracePartialBanner } from "../ChatTracePartialBanner";

describe("ChatTracePartialBanner", () => {
  it("says the trace is partial and lists each reason", () => {
    render(<ChatTracePartialBanner reasons={["Timing older than 7 days has expired.", "Model calls older than 90 days have expired."]} />);
    const banner = screen.getByRole("status");
    expect(banner).toHaveTextContent("Partial trace");
    expect(screen.getAllByTestId("chat-trace-partial-reason").map((li) => li.textContent)).toEqual([
      "Timing older than 7 days has expired.",
      "Model calls older than 90 days have expired.",
    ]);
  });

  it("renders nothing for a complete trace", () => {
    const { container } = render(<ChatTracePartialBanner reasons={[]} />);
    expect(container).toBeEmptyDOMElement();
  });
});
