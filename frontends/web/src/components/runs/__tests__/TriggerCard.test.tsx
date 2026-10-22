// Copyright (c) 2025 Reliant Labs

/**
 * The trigger card says what fired a non-interactive run, from its launch
 * event: the scheduled slot (in the automation's timezone), "Run now" for a
 * manual fire, the automation, and — expanded — the prompt and inputs it was
 * started with, read-only. It renders nothing for a run a person started, and
 * an older run with no launch event still says what it can.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { LaunchEvent } from "@/api/run-grpc";
import { TriggerCard } from "../TriggerCard";
import { githubTriggerTypes, renderRunsAt } from "./runTestUtils";

// The catalog's trigger types name an integration event.
vi.mock("@/api/catalog-search-grpc", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/catalog-search-grpc")>()),
  catalogSearchGrpc: { search: async () => githubTriggerTypes() },
}));

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
    const { container } = renderRunsAt(<TriggerCard launchKind="chat.start" />, "/workflows/runs/chat-1");
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
      "/workflows/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    // 08:00Z on 6 Oct 2026 is 09:00 BST.
    expect(card).toHaveTextContent("Scheduled for Tue 6 Oct, 09:00 (Europe/London) by Nightly triage");
    expect(card).toHaveTextContent("questions and approvals were answered automatically");
    expect(screen.getByRole("link", { name: "Nightly triage" })).toHaveAttribute("href", "/workflows/automations/trig-1");
  });

  it("says a fire ran late once it is more than a minute past its slot", async () => {
    renderRunsAt(
      <TriggerCard
        launchKind="schedule"
        triggerId="trig-1"
        triggerName="Nightly triage"
        event={scheduleEvent({ firedAt: "2026-10-06T08:04:10Z" })}
      />,
      "/workflows/runs/chat-1",
    );
    expect(await screen.findByTestId("trigger-card-late")).toHaveTextContent("Fired 4 min late (catch-up)");
  });

  it("stays quiet for a fire within a minute of its slot, or one with no fired_at", async () => {
    renderRunsAt(
      <TriggerCard launchKind="schedule" triggerName="N" event={scheduleEvent({ firedAt: "2026-10-06T08:00:50Z" })} />,
      "/workflows/runs/chat-1",
    );
    await screen.findByTestId("trigger-card");
    expect(screen.queryByTestId("trigger-card-late")).not.toBeInTheDocument();
  });

  it("does not call a fire without fired_at late", async () => {
    renderRunsAt(<TriggerCard launchKind="schedule" triggerName="N" event={scheduleEvent()} />, "/workflows/runs/chat-1");
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
      "/workflows/runs/chat-1",
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
      "/workflows/runs/chat-1",
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
      "/workflows/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Scheduled by Nightly triage");
    expect(screen.queryByRole("button", { name: "Show what it ran with" })).not.toBeInTheDocument();
  });

  it("an agent run names the parent chat, linked, and is not called unattended", async () => {
    renderRunsAt(
      <TriggerCard launchKind="agent.start_run" parent={{ chatId: "parent-1", title: "Refactor auth" }} />,
      "/workflows/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Started by an agent in Refactor auth");
    expect(screen.getByRole("link", { name: "Refactor auth" })).toHaveAttribute("href", "/workflows/runs/parent-1");
    expect(card).not.toHaveTextContent("answered automatically");
  });

  // Every automation runs with nobody behind it, not only a schedule.
  it.each(["webhook", "integration", "workflow_event"])(
    "a %s-launched run is called unattended",
    async (launchKind) => {
      renderRunsAt(
        <TriggerCard launchKind={launchKind} triggerId="trig-1" triggerName="On push" />,
        "/workflows/runs/chat-1",
      );
      const card = await screen.findByTestId("trigger-card");
      expect(card).toHaveTextContent("Unattended: questions and approvals were answered automatically");
    },
  );

  // The card names what fired a non-schedule automation run too, and links it.
  it.each([
    {
      launchKind: "webhook",
      event: { kind: "webhook", triggerId: "trig-2", occurredAt: "2026-10-06T14:02:00", manual: false },
      line: /Started by webhook On push at \d\d:\d\d/,
    },
    {
      launchKind: "integration",
      event: {
        kind: "integration",
        triggerId: "trig-2",
        occurredAt: "",
        manual: false,
        integration: "github",
        providerEvent: "pull_request.opened",
      },
      line: /Started by On push on GitHub: Pull request opened/,
    },
    {
      launchKind: "workflow_event",
      event: {
        kind: "workflow_event",
        triggerId: "trig-2",
        occurredAt: "",
        manual: false,
        sourceWorkflow: "code-review",
        sourceOutcome: "blocked",
      },
      line: /Started by On push when Code Review was blocked/,
    },
  ] satisfies { launchKind: string; event: LaunchEvent; line: RegExp }[])(
    "a $launchKind-launched run names its automation, linked, and what the source did",
    async ({ launchKind, event, line }) => {
      renderRunsAt(
        <TriggerCard launchKind={launchKind} triggerId="trig-2" triggerName="On push" event={event} />,
        "/workflows/runs/chat-1",
      );
      const card = await screen.findByTestId("trigger-card");
      await waitFor(() => expect(card.textContent).toMatch(line));
      expect(screen.getByRole("link", { name: "On push" })).toHaveAttribute("href", "/workflows/automations/trig-2");
    },
  );

  it("a workflow-event run whose automation was deleted keeps the name it fired under", async () => {
    renderRunsAt(
      <TriggerCard
        launchKind="workflow_event"
        event={{
          kind: "workflow_event",
          occurredAt: "",
          manual: false,
          triggerName: "After review",
          sourceWorkflow: "code-review",
          sourceOutcome: "finished",
        }}
      />,
      "/workflows/runs/chat-1",
    );
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Started by After review (since deleted) when Code Review finished");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("a builder test run is not called unattended", async () => {
    renderRunsAt(<TriggerCard launchKind="builder.test" />, "/workflows/runs/chat-1");
    const card = await screen.findByTestId("trigger-card");
    expect(card).not.toHaveTextContent("answered automatically");
  });

  it("an agent run whose parent is gone still says an agent started it", async () => {
    renderRunsAt(<TriggerCard launchKind="agent.start_run" />, "/workflows/runs/chat-1");
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Started by an agent");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});
