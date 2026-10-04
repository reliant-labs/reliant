// Copyright (c) 2025 Reliant Labs

/**
 * The trigger card says what fired a non-interactive run. It renders nothing
 * for a run a person started.
 */

import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";

import { TriggerCard } from "../TriggerCard";
import { renderRunsAt } from "./runTestUtils";

describe("TriggerCard", () => {
  it("renders nothing for an interactive chat", async () => {
    const { container } = renderRunsAt(<TriggerCard launchKind="chat.start" />, "/runs/chat-1");
    await screen.findByTestId("trigger-card-absent");
    expect(container.querySelector('[data-testid="trigger-card"]')).toBeNull();
  });

  it("a scheduled run names its automation and says it ran unattended", async () => {
    renderRunsAt(
      <TriggerCard launchKind="schedule" triggerId="trig-1" triggerName="Nightly triage" />,
      "/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Scheduled by Nightly triage");
    expect(card).toHaveTextContent("questions and approvals were answered automatically");
    expect(screen.getByRole("link", { name: "Nightly triage" })).toHaveAttribute("href", "/automations/trig-1");
  });

  it("an agent run is not called unattended", async () => {
    renderRunsAt(<TriggerCard launchKind="agent.start_run" />, "/runs/chat-1");
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Started by an agent");
    expect(card).not.toHaveTextContent("answered automatically");
  });
});
