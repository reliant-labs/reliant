// Copyright (c) 2025 Reliant Labs

/**
 * The trigger card says what fired a non-interactive run, from its launch
 * event: the scheduled slot (in the automation's timezone), "Run now" for a
 * manual fire, the automation, and — expanded — the prompt and inputs it was
 * started with, read-only. It renders nothing for a run a person started, and
 * an older run with no launch event still says what it can.
 */

import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { LaunchEvent } from "@/api/run-grpc";
import { TriggerCard } from "../TriggerCard";
import { renderRunsAt } from "./runTestUtils";

function scheduleEvent(overrides: Partial<LaunchEvent> = {}): LaunchEvent {
  return {
    kind: "schedule",
    triggerId: "trig-1",
    occurredAt: "2026-10-06T08:00:00Z",
    scheduledFor: "2026-10-06T08:00:00Z",
    triggerName: "Nightly triage",
    manual: false,
    start: {
      workflow: "triage",
      presets: { "": "careful" },
      params: { depth: 3, review: { strictness: "high" } },
    },
    ...overrides,
  };
}

describe("TriggerCard", () => {
  it("renders nothing for an interactive chat", async () => {
    const { container } = renderRunsAt(<TriggerCard launchKind="chat.start" />, "/runs/chat-1");
    await screen.findByTestId("trigger-card-absent");
    expect(container.querySelector('[data-testid="trigger-card"]')).toBeNull();
  });

  it("a scheduled run shows its slot in the automation's timezone and links the automation", async () => {
    renderRunsAt(
      <TriggerCard
        launchKind="schedule"
        triggerId="trig-1"
        triggerName="Nightly triage"
        timezone="Europe/London"
        event={scheduleEvent()}
      />,
      "/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    // 08:00Z on 6 Oct 2026 is 09:00 BST.
    expect(card).toHaveTextContent("Scheduled for Tue 6 Oct, 09:00 (Europe/London) by Nightly triage");
    expect(card).toHaveTextContent("questions and approvals were answered automatically");
    expect(screen.getByRole("link", { name: "Nightly triage" })).toHaveAttribute("href", "/automations/trig-1");
  });

  it("says a fire ran late once it is more than a minute past its slot", async () => {
    renderRunsAt(
      <TriggerCard
        launchKind="schedule"
        triggerId="trig-1"
        triggerName="Nightly triage"
        event={scheduleEvent({ firedAt: "2026-10-06T08:04:10Z" })}
      />,
      "/runs/chat-1",
    );
    expect(await screen.findByTestId("trigger-card-late")).toHaveTextContent("Fired 4 min late (catch-up)");
  });

  it("stays quiet for a fire within a minute of its slot, or one with no fired_at", async () => {
    renderRunsAt(
      <TriggerCard launchKind="schedule" triggerName="N" event={scheduleEvent({ firedAt: "2026-10-06T08:00:50Z" })} />,
      "/runs/chat-1",
    );
    await screen.findByTestId("trigger-card");
    expect(screen.queryByTestId("trigger-card-late")).not.toBeInTheDocument();
  });

  it("does not call a fire without fired_at late", async () => {
    renderRunsAt(<TriggerCard launchKind="schedule" triggerName="N" event={scheduleEvent()} />, "/runs/chat-1");
    await screen.findByTestId("trigger-card");
    expect(screen.queryByTestId("trigger-card-late")).not.toBeInTheDocument();
  });

  it("a manual fire says Run now, not a schedule slot", async () => {
    renderRunsAt(
      <TriggerCard
        launchKind="schedule"
        triggerId="trig-1"
        triggerName="Nightly triage"
        event={scheduleEvent({ manual: true })}
      />,
      "/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Run now on Nightly triage");
    expect(card).not.toHaveTextContent("Scheduled for");
  });

  it("expanded, shows the recorded prompt and inputs read-only", async () => {
    const user = userEvent.setup();
    renderRunsAt(
      <TriggerCard
        launchKind="schedule"
        triggerId="trig-1"
        triggerName="Nightly triage"
        event={scheduleEvent()}
        prompt="Triage the new issues"
      />,
      "/runs/chat-1",
    );
    const toggle = await screen.findByRole("button", { name: "Show what it ran with" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await user.click(toggle);
    const details = screen.getByTestId("trigger-card-details");
    expect(details).toHaveTextContent("Triage the new issues");
    expect(details).toHaveTextContent("careful");
    expect(details).toHaveTextContent("depth");
    expect(details).toHaveTextContent("3");
    expect(details).toHaveTextContent("review.strictness");
    expect(details).toHaveTextContent("high");
    // Read-only: nothing to type into.
    expect(details.querySelector("input, textarea, select")).toBeNull();
  });

  it("an older scheduled run with no launch event still names the automation, with no slot", async () => {
    renderRunsAt(
      <TriggerCard launchKind="schedule" triggerId="trig-1" triggerName="Nightly triage" event={null} />,
      "/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Scheduled by Nightly triage");
    expect(screen.queryByRole("button", { name: "Show what it ran with" })).not.toBeInTheDocument();
  });

  it("an agent run names the parent chat, linked, and is not called unattended", async () => {
    renderRunsAt(
      <TriggerCard launchKind="agent.start_run" parent={{ chatId: "parent-1", title: "Refactor auth" }} />,
      "/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Started by an agent in Refactor auth");
    expect(screen.getByRole("link", { name: "Refactor auth" })).toHaveAttribute("href", "/runs/parent-1");
    expect(card).not.toHaveTextContent("answered automatically");
  });

  it("an agent run whose parent is gone still says an agent started it", async () => {
    renderRunsAt(<TriggerCard launchKind="agent.start_run" />, "/runs/chat-1");
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Started by an agent");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
