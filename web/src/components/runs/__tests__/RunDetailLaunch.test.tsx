// Copyright (c) 2025 Reliant Labs

/**
 * Run detail, driven by the launch event (GetLaunchEvent):
 *
 *  - an agent-started run names and links its parent chat, and falls back to
 *    "Started by an agent" when the parent is gone or not the caller's;
 *  - "Re-run (current definition)" opens the Run… dialog prefilled with what
 *    the run was started with, and submitting it calls StartChat with those
 *    inputs, then opens the new (attended) chat in its project;
 *  - "Run automation now" fires the run's automation;
 *  - a chat from before launch events renders without errors, and offers no
 *    re-run it could not fill in.
 *
 * The RPC client is the boundary. loadRunContext (store wiring) and the
 * transcript are stubbed; everything between them and the wire is real.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  ChatSchema,
  ContentBlockSchema,
  ContentBlockType,
  GetChatResponseSchema,
  ListMessagesResponseSchema,
  MessageRole,
  MessageSchema,
  StartChatResponseSchema,
  WorkflowState,
  WorkflowStopReason,
  type Chat as ProtoChat,
} from "@/gen/reliant/v1/chat_pb";
import {
  FireTriggerResponseSchema,
  GetLaunchEventResponseSchema,
  ListTriggersResponseSchema,
  ScheduleSourceSchema,
  TriggerEventKind,
  TriggerEventSchema,
  TriggerSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  WORKFLOW_LIST,
  getWorkflowByName,
  presetsResponse,
  worktreesResponse,
} from "../../workflow/run/__tests__/runFormFixtures";
import { renderRunsAt } from "./runTestUtils";

const rpc = vi.hoisted(() => ({
  getChat: vi.fn(),
  startChat: vi.fn(),
  listMessages: vi.fn(),
  getLaunchEvent: vi.fn(),
  listTriggers: vi.fn(),
  fireTrigger: vi.fn(),
  listRuns: vi.fn(),
  listWorkflows: vi.fn(),
  getWorkflow: vi.fn(),
  listPresetsForWorkflow: vi.fn(),
  getDefaultPresetsBatch: vi.fn(),
  listWorktrees: vi.fn(),
  listDaemons: vi.fn(),
  listProjectDaemons: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    chat: () => ({ getChat: rpc.getChat, startChat: rpc.startChat, listMessages: rpc.listMessages }),
    trigger: () => ({
      getLaunchEvent: rpc.getLaunchEvent,
      listTriggers: rpc.listTriggers,
      fireTrigger: rpc.fireTrigger,
    }),
    run: () => ({ listRuns: rpc.listRuns }),
    workflow: () => ({ listWorkflows: rpc.listWorkflows, getWorkflow: rpc.getWorkflow }),
    preset: () => ({
      listPresetsForWorkflow: rpc.listPresetsForWorkflow,
      getDefaultPresetsBatch: rpc.getDefaultPresetsBatch,
    }),
    worktree: () => ({ listWorktrees: rpc.listWorktrees }),
    daemonRegistry: () => ({ listDaemons: rpc.listDaemons }),
    project: () => ({ listProjectDaemons: rpc.listProjectDaemons }),
  },
  setDaemonLastSeen: vi.fn(),
}));

// The run's chat is "loaded" straight from GetChat; the stores the real
// loader drives are not under test here.
const loadRunContext = vi.hoisted(() => vi.fn());
vi.mock("../loadRunContext", () => ({
  loadRunContext,
  RunProjectUnavailableError: class extends Error {},
}));
vi.mock("../../Chat/ChatContainer", () => ({
  ChatContainer: () => <div data-testid="transcript" />,
}));
vi.mock("../MachineStatus", () => ({ RunMachineBanner: () => null }));
vi.mock("@/store/globalUpdatesStore", () => ({
  useGlobalUpdatesStore: { getState: () => ({ connect: vi.fn() }) },
}));

import { RunDetail } from "../RunDetailPage";

function protoChat(overrides: Partial<ProtoChat> = {}) {
  return create(ChatSchema, {
    id: "chat-1",
    title: "Nightly triage · 2026-10-06 09:00",
    projectId: "proj-1",
    worktreeId: "wt-1",
    activeDaemonId: "daemon-1",
    workflowName: "triage",
    workflowState: WorkflowState.STOPPED,
    workflowStopReason: WorkflowStopReason.FAILED,
    createdAt: new Date(Date.now() - 60_000).toISOString(),
    launchKind: "schedule",
    triggerId: "trig-1",
    ...overrides,
  });
}

function chatOf(proto: ReturnType<typeof protoChat>) {
  const { $typeName: _, ...chat } = proto;
  return chat;
}

function scheduleLaunch(recordedPrompt?: string) {
  return create(GetLaunchEventResponseSchema, {
    event: create(TriggerEventSchema, {
      kind: TriggerEventKind.SCHEDULE,
      triggerId: "trig-1",
      occurredAt: "2026-10-06T08:00:00Z",
      chatId: "chat-1",
      payload: {
        scheduled_for: "2026-10-06T08:00:00Z",
        trigger_name: "Nightly triage",
        manual: false,
        start: {
          workflow: "triage",
          presets: { "": "careful" },
          params: { depth: 4, label: "bug", review: { strictness: "high" } },
          ...(recordedPrompt === undefined ? {} : { prompt: recordedPrompt }),
        },
      },
    }),
  });
}

function seedMessages(chatId: string, prompt: string) {
  return create(ListMessagesResponseSchema, {
    messages: [
      create(MessageSchema, {
        id: "sys",
        chatId,
        seq: 0n,
        role: MessageRole.SYSTEM,
        contentBlocks: [create(ContentBlockSchema, { type: ContentBlockType.TEXT, content: "No human is watching" })],
      }),
      create(MessageSchema, {
        id: "user",
        chatId,
        seq: 1n,
        role: MessageRole.USER,
        contentBlocks: [create(ContentBlockSchema, { type: ContentBlockType.TEXT, content: prompt })],
      }),
    ],
  });
}

function useChat(proto: ReturnType<typeof protoChat>, others: Record<string, ReturnType<typeof protoChat>> = {}) {
  loadRunContext.mockResolvedValue(chatOf(proto));
  rpc.getChat.mockImplementation(async ({ chatId }: { chatId: string }) => {
    if (chatId === proto.id) return create(GetChatResponseSchema, { chat: proto });
    const other = others[chatId];
    if (other) return create(GetChatResponseSchema, { chat: other });
    throw new ConnectError("chat not found", Code.NotFound);
  });
}

describe("run detail: launch event", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    rpc.listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        triggers: [
          create(TriggerSchema, {
            id: "trig-1",
            name: "Nightly triage",
            projectId: "proj-1",
            source: {
              case: "schedule",
              value: create(ScheduleSourceSchema, { cron: ["0 9 * * *"], timezone: "Europe/London" }),
            },
          }),
        ],
      }),
    );
    rpc.listRuns.mockResolvedValue({ runs: [], nextPageToken: "" });
    rpc.listMessages.mockImplementation(async ({ chatId }: { chatId: string }) =>
      seedMessages(chatId, "Triage the new issues"),
    );
    rpc.listWorkflows.mockResolvedValue(WORKFLOW_LIST);
    rpc.getWorkflow.mockImplementation(async (request: { name: string }) => getWorkflowByName(request));
    rpc.listPresetsForWorkflow.mockResolvedValue(presetsResponse());
    rpc.getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
    rpc.listWorktrees.mockImplementation(async (request: { projectId: string }) =>
      worktreesResponse(request.projectId),
    );
    rpc.listDaemons.mockResolvedValue({
      daemons: [create(DaemonInfoSchema, { daemonId: "daemon-1", hostname: "laptop", status: DaemonStatus.ACTIVE })],
    });
    rpc.listProjectDaemons.mockResolvedValue({ projectDaemons: [] });
    rpc.startChat.mockResolvedValue(
      create(StartChatResponseSchema, {
        chat: create(ChatSchema, { id: "chat-new", projectId: "proj-1", title: "Re-run" }),
        workflowId: "wf-new",
        runId: "run-new",
      }),
    );
    rpc.fireTrigger.mockResolvedValue(create(FireTriggerResponseSchema, { fireWorkflowId: "fire-1" }));
  });

  it("an agent-started run names its parent chat and links to it", async () => {
    useChat(protoChat({ launchKind: "agent.start_run", triggerId: undefined }), {
      "parent-1": protoChat({ id: "parent-1", title: "Refactor auth", launchKind: "chat.start" }),
    });
    rpc.getLaunchEvent.mockResolvedValue(
      create(GetLaunchEventResponseSchema, {
        event: create(TriggerEventSchema, {
          kind: TriggerEventKind.AGENT_START_RUN,
          payload: { parent_chat_id: "parent-1", start: { workflow: "triage", presets: {}, params: {} } },
        }),
      }),
    );
    renderRunsAt(<RunDetail chatId="chat-1" />, "/workflows/runs/chat-1");

    const line = await screen.findByTestId("run-started-by");
    await waitFor(() => expect(line).toHaveTextContent("Started by an agent in Refactor auth"));
    const links = screen.getAllByRole("link", { name: "Refactor auth" });
    expect(links.length).toBeGreaterThan(0);
    for (const link of links) expect(link).toHaveAttribute("href", "/workflows/runs/parent-1");
  });

  it("falls back to 'Started by an agent' when the parent is gone or not the caller's", async () => {
    useChat(protoChat({ launchKind: "agent.start_run", triggerId: undefined }));
    rpc.getLaunchEvent.mockResolvedValue(
      create(GetLaunchEventResponseSchema, {
        event: create(TriggerEventSchema, {
          kind: TriggerEventKind.AGENT_START_RUN,
          payload: { parent_chat_id: "someone-elses-chat" },
        }),
      }),
    );
    renderRunsAt(<RunDetail chatId="chat-1" />, "/workflows/runs/chat-1");

    await screen.findByTestId("trigger-card");
    expect(screen.getByTestId("run-started-by")).toHaveTextContent(/^Started by an agent$/);
    expect(rpc.getChat).toHaveBeenCalledWith(expect.objectContaining({ chatId: "someone-elses-chat" }));
    expect(screen.queryByRole("link", { name: /another run/ })).not.toBeInTheDocument();
  });

  it("Re-run prefers the prompt the launch recorded over the transcript's first message", async () => {
    const user = userEvent.setup();
    useChat(protoChat());
    rpc.getLaunchEvent.mockResolvedValue(scheduleLaunch("The recorded prompt"));
    renderRunsAt(<RunDetail chatId="chat-1" />, "/workflows/runs/chat-1");

    await user.click(await screen.findByRole("button", { name: "Re-run (current definition)" }));
    await screen.findByRole("form", { name: "Re-run (current definition)" });
    expect(screen.getByLabelText("Message")).toHaveValue("The recorded prompt");
  });

  it("Re-run (current definition) prefills the Run… dialog and starts an attended chat with the recorded inputs", async () => {
    const user = userEvent.setup();
    useChat(protoChat());
    rpc.getLaunchEvent.mockResolvedValue(scheduleLaunch());
    const { router } = renderRunsAt(<RunDetail chatId="chat-1" />, "/workflows/runs/chat-1");

    // The card shows the slot in the automation's timezone once the event loads.
    expect(await screen.findByTestId("trigger-card")).toHaveTextContent(
      "Scheduled for Tue 6 Oct, 09:00 (Europe/London) by Nightly triage",
    );

    const rerun = await screen.findByRole("button", { name: "Re-run (current definition)" });
    await user.click(rerun);

    const dialog = await screen.findByRole("form", { name: "Re-run (current definition)" });
    expect(dialog).toBeInTheDocument();
    expect(screen.getByLabelText("Message")).toHaveValue("Triage the new issues");
    await waitFor(() => expect(screen.getByLabelText("Depth")).toHaveValue(4));
    expect(screen.getByLabelText("Label")).toHaveValue("bug");
    expect(screen.getByLabelText("Strictness")).toHaveValue("high");
    await waitFor(() => expect(screen.getByLabelText("Workspace")).toHaveValue("wt-1"));
    await waitFor(() => expect(screen.getByLabelText("Runs on")).toHaveValue("daemon-1"));

    await user.click(screen.getByRole("button", { name: "Re-run" }));

    await waitFor(() => expect(rpc.startChat).toHaveBeenCalledTimes(1));
    const request = rpc.startChat.mock.calls[0]![0];
    expect(request).toMatchObject({
      projectId: "proj-1",
      worktreeId: "wt-1",
      daemonId: "daemon-1",
      workflow: "triage",
      selectedPresets: { "": "careful" },
    });
    expect(request.messages[0].content).toBe("Triage the new issues");
    expect(request.workflowParams.depth.kind).toEqual({ case: "numberValue", value: 4 });
    expect(request.workflowParams.label.kind).toEqual({ case: "stringValue", value: "bug" });
    expect(request.workflowParams.review.kind.value.fields.strictness.kind).toEqual({
      case: "stringValue",
      value: "high",
    });

    // Attended: the new chat opens as a chat in its project, not in Runs.
    await waitFor(() => expect(loadRunContext).toHaveBeenCalledWith("chat-new"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/project/proj-1"));
  });

  it("Run automation now fires the run's automation", async () => {
    const user = userEvent.setup();
    useChat(protoChat());
    rpc.getLaunchEvent.mockResolvedValue(scheduleLaunch());
    renderRunsAt(<RunDetail chatId="chat-1" />, "/workflows/runs/chat-1");

    await user.click(await screen.findByRole("button", { name: "Run automation now" }));
    await waitFor(() => expect(rpc.fireTrigger).toHaveBeenCalledTimes(1));
    expect(rpc.fireTrigger.mock.calls[0]![0]).toMatchObject({ id: "trig-1" });
    expect(rpc.startChat).not.toHaveBeenCalled();
  });

  it("an old chat with no launch event renders without errors and offers no re-run", async () => {
    const consoleError = vi.spyOn(console, "error");
    useChat(protoChat());
    rpc.getLaunchEvent.mockResolvedValue(create(GetLaunchEventResponseSchema, {}));
    renderRunsAt(<RunDetail chatId="chat-1" />, "/workflows/runs/chat-1");

    await screen.findByTestId("transcript");
    const card = await screen.findByTestId("trigger-card");
    expect(card).toHaveTextContent("Scheduled by Nightly triage");
    expect(card).not.toHaveTextContent("Scheduled for");
    expect(screen.queryByRole("button", { name: "Re-run (current definition)" })).not.toBeInTheDocument();
    // Nothing it cannot fill in is fetched.
    expect(rpc.listMessages).not.toHaveBeenCalled();
    expect(consoleError).not.toHaveBeenCalled();
    consoleError.mockRestore();
  });
});
