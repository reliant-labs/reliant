// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import Status from "../status";

describe("Status", () => {
  it("is a coloured dot and a word by default", () => {
    const { container } = render(<Status tone="danger" label="Drifted" />);
    expect(screen.getByText("Drifted")).toBeInTheDocument();
    expect(container.querySelector("[data-status-spinner]")).toBeNull();
    expect(container.querySelector(".bg-danger")).not.toBeNull();
  });

  it("is a neutral spinning ring of the dot's size while busy, whatever the tone", () => {
    const { container } = render(<Status tone="danger" label="Settling" size="sm" busy />);
    const ring = container.querySelector("[data-status-spinner]");
    expect(ring).not.toBeNull();
    expect(ring).toHaveAttribute("aria-hidden", "true");
    expect(ring?.className).toContain("animate-spin");
    expect(ring?.className).toContain("h-1.5 w-1.5");
    expect(ring?.className).toContain("border-ink-muted");
    // No status hue on the ring, and no dot beside it.
    expect(container.querySelector(".bg-danger")).toBeNull();
    expect(container.firstElementChild).toHaveAttribute("data-busy", "true");
  });

  it("holds still as a whole ring under reduced motion", () => {
    const { container } = render(<Status label="Settling" busy />);
    const ring = container.querySelector("[data-status-spinner]");
    expect(ring?.className).toContain("motion-reduce:animate-none");
    expect(ring?.className).toContain("motion-reduce:border-t-ink-muted/40");
  });

  it("keeps the word for screen readers when only the spinner shows", () => {
    render(<Status label="Settling" hideLabel busy />);
    expect(screen.getByText("Settling")).toHaveClass("sr-only");
  });
});
