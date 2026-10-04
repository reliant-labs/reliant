// Copyright (c) 2025 Reliant Labs

/**
 * The automation page's actions: Run now calls FireTrigger, confirms with a
 * toast and refetches the history; Open chat hands the launched chat to the
 * opener; Delete asks first.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  FireTriggerResponseSchema,
  GetTriggerResponseSchema,
  ListTriggerEventsResponseSchema,
  ScheduleSourceSchema,
  TriggerEventOutcome,
  TriggerEventSchema,
  TriggerSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  ChatActivity,
  ChatSchema,
  GetChatResponseSchema,
  WorkflowState,
  WorkflowStopReason,
} from "@/gen/reliant/v1/chat_pb";
import { HOUR, isoFromNow, renderAtRoute } from "./automationTestUtils";

const getTrigger = vi.fn();
const listTriggerEvents = vi.fn();
const fireTrigger = vi.fn();
const deleteTrigger = vi.fn();
const getChat = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    trigger: () => ({ getTrigger, listTriggerEvents, fireTrigger, deleteTrigger }),
    chat: () => ({ getChat }),
    daemonRegistry: () => ({
      listDaemons: vi.fn(async () => ({
        daemons: [create(DaemonInfoSchema, { daemonId: "daemon-1", hostname: "laptop", status: DaemonStatus.IDLE })],
      })),
    }),
    workflow: () => ({ listWorkflows: vi.fn(async () => ({ workflows: [], invalidWorkflows: [] })) }),
  },
}));

const toastSuccess = vi.fn();
const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  }),
}));

const openAutomationChat = vi.fn(async () => undefined);
vi.mock("../openAutomationChat", () => ({
  openAutomationChat: (...args: unknown[]) => openAutomationChat(...(args as [])),
}));

vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({
    isElectron: false,
    isMac: false,
    isFullscreen: false,
    trafficLightPadding: "8px",
    dragRegionStyle: {},
    noDragRegionStyle: {},
  }),
}));

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({
    projects: [{ id: "proj-1", name: "Reliant" }],
    currentProject: null,
    loadProjects: vi.fn(async () => undefined),
  });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { AutomationDetail } from "../AutomationDetailPage";

const trigger = create(TriggerSchema, {
  id: "trig-1",
  name: "Morning triage",
  projectId: "proj-1",
  enabled: true,
  workflow: "builtin://agent",
  message: "Triage new issues",
  daemonId: "daemon-1",
  nextFireAt: isoFromNow(5 * HOUR),
  source: {
    case: "schedule",
    value: create(ScheduleSourceSchema, { cron: ["0 9 * * 1-5"], timezone: "America/New_York" }),
  },
});

const launched = create(TriggerEventSchema, {
  id: "ev-1",
  triggerId: "trig-1",
  occurredAt: isoFromNow(-HOUR),
  outcome: TriggerEventOutcome.LAUNCHED,
  chatId: "chat-42",
});

const skipped = create(TriggerEventSchema, {
  id: "ev-0",
  triggerId: "trig-1",
  occurredAt: isoFromNow(-25 * HOUR),
  outcome: TriggerEventOutcome.SKIPPED,
  outcomeDetail: "previous run still active",
});

/** The chat a launched event started, with its root run's lifecycle. */
function launchedChat(state: WorkflowState, stopReason: WorkflowStopReason, activity = ChatActivity.IDLE) {
  return create(GetChatResponseSchema, {
    chat: create(ChatSchema, {
      id: "chat-42",
      projectId: "proj-1",
      workflowState: state,
      workflowStopReason: stopReason,
      activity,
      launchKind: "schedule",
      triggerId: "trig-1",
    }),
  });
}

describe("AutomationDetail", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getTrigger.mockResolvedValue(create(GetTriggerResponseSchema, { trigger }));
    listTriggerEvents.mockResolvedValue(
      create(ListTriggerEventsResponseSchema, { events: [launched, skipped] }),
    );
    getChat.mockResolvedValue(launchedChat(WorkflowState.STOPPED, WorkflowStopReason.COMPLETED));
  });

  it("a run that launched and then failed does not read as green", async () => {
    getChat.mockResolvedValue(launchedChat(WorkflowState.STOPPED, WorkflowStopReason.FAILED, ChatActivity.ERROR));
    renderAtRoute(<AutomationDetail triggerId="trig-1" />, "/automations/trig-1");

    const row = await screen.findByTestId("automation-event-ev-1");
    // The firing keeps its own word, in a color that claims nothing about the run.
    expect(within(row).getByText("Launched")).toBeInTheDocument();
    // The run's own status is what the row reports as its result.
    expect(await within(row).findByText("Failed")).toBeInTheDocument();
    expect(getChat.mock.calls[0]![0]).toMatchObject({ chatId: "chat-42" });
    // Asserted on the rendered pills, not on a test hook: green is the class.
    expect(row.querySelector('[class*="bg-success"]')).toBeNull();
    // A firing that launched nothing has no run to look up.
    expect(getChat).toHaveBeenCalledTimes(1);
  });

  it("shows the launched run's live status", async () => {
    getChat.mockResolvedValue(
      launchedChat(WorkflowState.ACTIVE, WorkflowStopReason.UNSPECIFIED, ChatActivity.AWAITING_INPUT),
    );
    renderAtRoute(<AutomationDetail triggerId="trig-1" />, "/automations/trig-1");

    const row = await screen.findByTestId("automation-event-ev-1");
    expect(await within(row).findByText("Needs you")).toBeInTheDocument();
  });

  it("says so when the launched chat can no longer be read", async () => {
    getChat.mockRejectedValue(new Error("chat not found"));
    renderAtRoute(<AutomationDetail triggerId="trig-1" />, "/automations/trig-1");

    const row = await screen.findByTestId("automation-event-ev-1");
    expect(await within(row).findByText("Unavailable")).toBeInTheDocument();
  });

  it("renders the definition and the event history", async () => {
    renderAtRoute(<AutomationDetail triggerId="trig-1" />, "/automations/trig-1");

    expect(await screen.findByRole("heading", { name: "Morning triage" })).toBeInTheDocument();
    expect(screen.getByText("Triage new issues")).toBeInTheDocument();
    expect(await screen.findByText("laptop")).toBeInTheDocument();
    expect(screen.getByText("(idle)")).toBeInTheDocument();
    const launchedRow = await screen.findByTestId("automation-event-ev-1");
    expect(within(launchedRow).getByText("Launched")).toBeInTheDocument();
    expect(within(launchedRow).getByRole("button", { name: "Open chat" })).toBeInTheDocument();
    const skippedRow = screen.getByTestId("automation-event-ev-0");
    expect(within(skippedRow).getByText("Skipped")).toBeInTheDocument();
    expect(within(skippedRow).getByText("previous run still active")).toBeInTheDocument();
    expect(within(skippedRow).queryByRole("button", { name: "Open chat" })).not.toBeInTheDocument();
    expect(listTriggerEvents.mock.calls[0]![0]).toMatchObject({ triggerId: "trig-1" });
  });

  it("Run now calls FireTrigger, toasts, and refetches the history", async () => {
    fireTrigger.mockResolvedValue(
      create(FireTriggerResponseSchema, { fireWorkflowId: "trigger-fire-trig-1-manual" }),
    );
    const user = userEvent.setup();
    renderAtRoute(<AutomationDetail triggerId="trig-1" />, "/automations/trig-1");
    await screen.findByTestId("automation-event-ev-1");
    const eventCallsBefore = listTriggerEvents.mock.calls.length;

    await user.click(screen.getByRole("button", { name: "Run now" }));

    await waitFor(() => expect(fireTrigger).toHaveBeenCalledTimes(1));
    expect(fireTrigger.mock.calls[0]![0]).toMatchObject({ id: "trig-1" });
    await waitFor(() =>
      expect(toastSuccess).toHaveBeenCalledWith("Morning triage started", expect.any(Object)),
    );
    await waitFor(() => expect(listTriggerEvents.mock.calls.length).toBeGreaterThan(eventCallsBefore));
    expect(screen.getByRole("status")).toHaveTextContent("Waiting for the run to start");
  });

  it("reports a failed Run now", async () => {
    fireTrigger.mockRejectedValue(new Error("temporal unavailable"));
    const user = userEvent.setup();
    renderAtRoute(<AutomationDetail triggerId="trig-1" />, "/automations/trig-1");

    await user.click(await screen.findByRole("button", { name: "Run now" }));

    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith("Could not run Morning triage", {
        description: "temporal unavailable",
      }),
    );
  });

  it("opens a launched chat in the automation's project", async () => {
    const user = userEvent.setup();
    renderAtRoute(<AutomationDetail triggerId="trig-1" />, "/automations/trig-1");

    const row = await screen.findByTestId("automation-event-ev-1");
    await user.click(within(row).getByRole("button", { name: "Open chat" }));

    await waitFor(() => expect(openAutomationChat).toHaveBeenCalledTimes(1));
    expect(openAutomationChat.mock.calls[0]).toEqual(["chat-42", "proj-1", expect.any(Function)]);
  });

  it("asks before deleting", async () => {
    deleteTrigger.mockResolvedValue({});
    const user = userEvent.setup();
    renderAtRoute(<AutomationDetail triggerId="trig-1" />, "/automations/trig-1");

    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(deleteTrigger).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "Delete Morning triage?" });
    await user.click(within(dialog).getByRole("button", { name: "Delete automation" }));

    await waitFor(() => expect(deleteTrigger).toHaveBeenCalledTimes(1));
    expect(deleteTrigger.mock.calls[0]![0]).toMatchObject({ id: "trig-1" });
  });
});
