// Copyright (c) 2025 Reliant Labs

/**
 * The verdict chip spins while a change is in flight — Settling, or a deploy
 * the surface knows is running — and never over a known-bad verdict.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { VerdictChip } from "../HostedWorkloads";

function chip() {
  const el = screen.getByTestId("chip");
  return { el, icon: el.querySelector("svg") };
}

describe("VerdictChip in flight", () => {
  it("spins while settling", () => {
    render(<VerdictChip verdict="converging" testId="chip" />);
    const { el, icon } = chip();
    expect(el).toHaveTextContent("Settling");
    expect(el).toHaveAttribute("data-busy", "true");
    expect(icon?.getAttribute("class")).toContain("animate-spin");
    expect(icon?.getAttribute("class")).toContain("motion-reduce:animate-none");
  });

  it("spins a healthy environment whose deploy is running, keeping its word", () => {
    render(<VerdictChip verdict="converged" deploying testId="chip" />);
    const { el, icon } = chip();
    expect(el).toHaveTextContent("Healthy");
    expect(el).toHaveAttribute("data-busy", "true");
    expect(icon?.getAttribute("class")).toContain("animate-spin");
  });

  it("keeps a drifted environment's warning glyph still, even mid-deploy", () => {
    render(<VerdictChip verdict="diverged" deploying testId="chip" />);
    const { el, icon } = chip();
    expect(el).toHaveTextContent("Drifted");
    expect(el).not.toHaveAttribute("data-busy");
    expect(icon?.getAttribute("class")).not.toContain("animate-spin");
  });

  it("holds still when converged and nothing is deploying", () => {
    render(<VerdictChip verdict="converged" testId="chip" />);
    expect(chip().el).not.toHaveAttribute("data-busy");
  });
});
