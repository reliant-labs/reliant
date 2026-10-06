// Copyright (c) 2025 Reliant Labs

/**
 * The run detail header: status from runStatus, who started the run (with a
 * link to what started it), and the actions the run's state allows.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ChatActivity, WorkflowState, WorkflowStopReason } from "@/gen/reliant/v1/chat_pb";
import type { Chat } from "@/api/client";
import type { LaunchEvent } from "@/api/run-grpc";
import { RunHeader, type RunHeaderActions } from "../RunHeader";
import { renderRunsAt } from "./runTestUtils";

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ daemons: [{ daemonId: "d-1", hostname: "laptop" }] }),
}));

function chat(overrides: Partial<Chat> = {}): Chat {
  return {
    id: "chat-1",
    userId: "u",
    title: "Nightly triage",
    projectId: "proj-1",
    state: 0,
    createdAt: new Date(Date.now() - 5 * 60_000).toISOString(),
    updatedAt: "",
    lastActive: "",
    selectedPresets: {},
    needsRecovery: false,
    activity: ChatActivity.IDLE,
    unread: false,
    workflowName: "builtin://agent",
    workflowState: WorkflowState.STOPPED,
    workflowStopReason: WorkflowStopReason.COMPLETED,
    launchKind: "chat.start",
    ...overrides,
  } as Chat;
}

function launchEvent(overrides: Partial<LaunchEvent>): LaunchEvent {
  return { kind: "", occurredAt: "", manual: false, ...overrides };
}

function actions(): RunHeaderActions {
  return {
    onPause: vi.fn(),
    onResume: vi.fn(),
    onStop: vi.fn(),
    onOpenAsChat: vi.fn(),
    onToggleDiagram: vi.fn(),
  };
}

describe("RunHeader", () => {
  let handlers: RunHeaderActions;
  beforeEach(() => {
    handlers = actions();
  });

  it("shows the run's status in the runStatus vocabulary", async () => {
    renderRunsAt(
      <RunHeader
        chat={chat({ workflowState: WorkflowState.STOPPED, workflowStopReason: WorkflowStopReason.FAILED })}
        projectName="Reliant"
        actions={handlers}
      />,
      "/workflows/runs/chat-1",
    );
    expect(await screen.findByRole("heading", { name: "Nightly triage" })).toBeInTheDocument();
    expect(screen.getByText("Failed")).toBeInTheDocument();
  });

  it("an agent-started run says so, and names the parent chat when it is known", async () => {
    renderRunsAt(
      <RunHeader
        chat={chat({ launchKind: "agent.start_run" })}
        parent={{ chatId: "parent-1", title: "Refactor auth" }}
        projectName="Reliant"
        actions={handlers}
      />,
      "/workflows/runs/chat-1",
    );
    const line = await screen.findByTestId("run-started-by");
    expect(line).toHaveTextContent("Started by an agent in Refactor auth");
    expect(screen.getByRole("link", { name: "Refactor auth" })).toHaveAttribute("href", "/workflows/runs/parent-1");
  });

  it("an agent-started run without a known parent still says an agent started it", async () => {
    renderRunsAt(<RunHeader chat={chat({ launchKind: "agent.start_run" })} actions={handlers} />, "/workflows/runs/chat-1");
    expect(await screen.findByTestId("run-started-by")).toHaveTextContent("Started by an agent");
  });

  it("a scheduled run links to its automation", async () => {
    renderRunsAt(
      <RunHeader
        chat={chat({ launchKind: "schedule", triggerId: "trig-1" })}
        triggerName="Hourly sweep"
        actions={handlers}
      />,
      "/workflows/runs/chat-1",
    );
    expect(await screen.findByTestId("run-started-by")).toHaveTextContent("Started by schedule Hourly sweep");
    expect(screen.getByRole("link", { name: "Hourly sweep" })).toHaveAttribute("href", "/workflows/automations/trig-1");
  });

  // Every kind an automation fires names that automation and links to it, with
  // what the source did (§0 launch-kind vocabulary).
  it.each([
    {
      launchKind: "webhook",
      event: launchEvent({ kind: "webhook", occurredAt: "2026-10-06T14:02:00" }),
      line: /^Started by webhook deploy-hook at \d\d:\d\d$/,
      name: "deploy-hook",
    },
    {
      launchKind: "integration",
      event: launchEvent({ kind: "integration", integration: "github", providerEvent: "issues.opened" }),
      line: /^Started by deploy-hook on github: issues\.opened$/,
      name: "deploy-hook",
    },
    {
      launchKind: "workflow_event",
      event: launchEvent({ kind: "workflow_event", sourceWorkflow: "code-review", sourceOutcome: "failed" }),
      // The source workflow reads by its display name.
      line: /^Started by deploy-hook when Code Review failed$/,
      name: "deploy-hook",
    },
  ])("a $launchKind-launched run names and links its automation", async ({ launchKind, event, line, name }) => {
    renderRunsAt(
      <RunHeader chat={chat({ launchKind, triggerId: "trig-2" })} triggerName={name} event={event} actions={handlers} />,
      "/workflows/runs/chat-1",
    );
    expect((await screen.findByTestId("run-started-by")).textContent).toMatch(line);
    expect(screen.getByRole("link", { name })).toHaveAttribute("href", "/workflows/automations/trig-2");
  });

  it("a Run now of a schedule says so and still links the automation", async () => {
    renderRunsAt(
      <RunHeader
        chat={chat({ launchKind: "schedule", triggerId: "trig-1" })}
        triggerName="Hourly sweep"
        event={launchEvent({ kind: "schedule", manual: true })}
        actions={handlers}
      />,
      "/workflows/runs/chat-1",
    );
    expect(await screen.findByTestId("run-started-by")).toHaveTextContent("Started by Run now on Hourly sweep");
    expect(screen.getByRole("link", { name: "Hourly sweep" })).toHaveAttribute("href", "/workflows/automations/trig-1");
  });

  it("a webhook run whose automation is gone says what it can, unlinked", async () => {
    renderRunsAt(
      <RunHeader chat={chat({ launchKind: "webhook" })} event={launchEvent({ kind: "webhook" })} actions={handlers} />,
      "/workflows/runs/chat-1",
    );
    expect(await screen.findByTestId("run-started-by")).toHaveTextContent(/^Started by a webhook/);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("a run whose automation was deleted says so", async () => {
    renderRunsAt(<RunHeader chat={chat({ launchKind: "schedule" })} actions={handlers} />, "/workflows/runs/chat-1");
    expect(await screen.findByTestId("run-started-by")).toHaveTextContent(
      "Started by a schedule that has since been deleted",
    );
  });

  it("a running run offers Pause and Stop", async () => {
    const user = userEvent.setup();
    renderRunsAt(
      <RunHeader
        chat={chat({ workflowState: WorkflowState.ACTIVE, workflowStopReason: WorkflowStopReason.UNSPECIFIED, activity: ChatActivity.RUNNING })}
        actions={handlers}
      />,
      "/workflows/runs/chat-1",
    );
    await user.click(await screen.findByRole("button", { name: "Pause" }));
    expect(handlers.onPause).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "Stop" }));
    expect(handlers.onStop).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "Resume" })).not.toBeInTheDocument();
  });

  it("a paused run offers Resume", async () => {
    const user = userEvent.setup();
    renderRunsAt(
      <RunHeader chat={chat({ workflowStopReason: WorkflowStopReason.PAUSED })} actions={handlers} />,
      "/workflows/runs/chat-1",
    );
    await user.click(await screen.findByRole("button", { name: "Resume" }));
    expect(handlers.onResume).toHaveBeenCalledTimes(1);
  });

  it("offers Open as chat on an automation run that has not been adopted", async () => {
    const user = userEvent.setup();
    renderRunsAt(<RunHeader chat={chat({ launchKind: "schedule" })} actions={handlers} />, "/workflows/runs/chat-1");
    await user.click(await screen.findByRole("button", { name: "Open as chat" }));
    await waitFor(() => expect(handlers.onOpenAsChat).toHaveBeenCalledTimes(1));
  });

  it("an interactive chat, or an adopted run, is already a chat", async () => {
    const { unmount } = renderRunsAt(<RunHeader chat={chat()} actions={handlers} />, "/workflows/runs/chat-1");
    await screen.findByRole("heading", { name: "Nightly triage" });
    expect(screen.getByRole("button", { name: "Open chat" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open as chat" })).not.toBeInTheDocument();
    unmount();

    renderRunsAt(
      <RunHeader chat={chat({ launchKind: "schedule", adoptedAt: new Date().toISOString() })} actions={handlers} />,
      "/workflows/runs/chat-1",
    );
    await screen.findByRole("heading", { name: "Nightly triage" });
    expect(screen.queryByRole("button", { name: "Open as chat" })).not.toBeInTheDocument();
  });

  it("names the machine", async () => {
    renderRunsAt(<RunHeader chat={chat({ activeDaemonId: "d-1" })} projectName="Reliant" actions={handlers} />, "/workflows/runs/chat-1");
    expect(await screen.findByText(/Reliant · laptop/)).toBeInTheDocument();
  });
});
