// Copyright (c) 2025 Reliant Labs

/**
 * The getting-started frame: what the page shows first, then numbered steps
 * whose commands copy whole, then the docs. A note, never an alert.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { Rocket } from "lucide-react";

import { GettingStarted } from "../GettingStarted";

afterEach(() => vi.restoreAllMocks());

function renderFrame() {
  return render(
    <GettingStarted
      testId="gs"
      icon={<Rocket className="h-4 w-4" />}
      title="Nothing here yet"
      about="This page shows the thing."
      steps={[{ text: "Deploy it.", command: "forge env deploy prod --from staging" }, { text: "Then wait." }]}
      docs={{ href: "https://docs.reliantlabs.io/forge/observability", label: "Read the docs" }}
    >
      <p>Why it is empty.</p>
    </GettingStarted>
  );
}

describe("GettingStarted", () => {
  it("says what the page shows, then the steps in order, then the docs", () => {
    renderFrame();
    const frame = screen.getByTestId("gs");
    expect(frame).toHaveAttribute("role", "status");
    expect(within(frame).getByTestId("gs-about")).toHaveTextContent("This page shows the thing.");
    const steps = within(frame).getByRole("list", { name: "Get started" });
    expect(within(steps).getAllByRole("listitem").map((item) => item.textContent)).toEqual([
      expect.stringContaining("Deploy it."),
      expect.stringContaining("Then wait."),
    ]);
    // A command is shown whole, never truncated with an ellipsis.
    expect(within(steps).getByText("forge env deploy prod --from staging")).toBeInTheDocument();
    const docs = within(frame).getByTestId("gs-docs");
    expect(docs).toHaveAttribute("href", "https://docs.reliantlabs.io/forge/observability");
    expect(docs).toHaveAttribute("target", "_blank");
    expect(docs).toHaveAttribute("rel", "noreferrer");
  });

  it("copies a command exactly, and says so", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    renderFrame();
    fireEvent.click(screen.getByRole("button", { name: "Copy forge env deploy prod --from staging" }));
    expect(writeText).toHaveBeenCalledWith("forge env deploy prod --from staging");
    await waitFor(() => expect(screen.getByRole("button", { name: "Copied forge env deploy prod --from staging" })).toBeInTheDocument());
  });

  it("leaves the command on screen when there is no clipboard", async () => {
    const writeText = vi.fn().mockRejectedValue(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    renderFrame();
    fireEvent.click(screen.getByRole("button", { name: "Copy forge env deploy prod --from staging" }));
    await waitFor(() => expect(writeText).toHaveBeenCalled());
    expect(screen.getByRole("button", { name: "Copy forge env deploy prod --from staging" })).toBeInTheDocument();
  });
});
