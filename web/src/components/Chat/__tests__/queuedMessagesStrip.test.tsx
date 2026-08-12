/**
 * The pending-queue strip, wired into the real composer.
 *
 * QueuedMessages.test.tsx pins the strip's own behavior against props. This
 * pins the wiring, which is where the user's actual complaint lived: they
 * queued a message, got a toast, and saw nothing. Two things have to hold for
 * that to be fixed:
 *
 *  - the strip reads the mailbox of the thread the composer queues INTO, so a
 *    queued message shows up above the tray at all.
 *  - queueing refreshes it immediately, so the message appears WITH the toast
 *    rather than up to a poll interval later.
 *
 * The composer is mounted streaming, because that is the only state in which
 * queueing exists.
 */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useAttachmentStore } from "../../../store/attachmentStore";

const CHAT_ID = "chat-abc";
const MAIN_THREAD_ID = "workflow-xyz";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const {
  sendAgentMessageMock,
  listQueuedAgentMessagesMock,
  cancelQueuedAgentMessageMock,
  toastErrorMock,
  toastInfoMock,
} = vi.hoisted(() => ({
  sendAgentMessageMock: vi.fn(),
  listQueuedAgentMessagesMock: vi.fn(),
  cancelQueuedAgentMessageMock: vi.fn(),
  toastErrorMock: vi.fn(),
  toastInfoMock: vi.fn(),
}));

vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: {
    sendAgentMessage: sendAgentMessageMock,
    listQueuedAgentMessages: listQueuedAgentMessagesMock,
    cancelQueuedAgentMessage: cancelQueuedAgentMessageMock,
  },
  QUEUED_SENDER_KIND_HUMAN: 5,
}));

vi.mock("../../../lib/toast-manager", () => ({
  toast: {
    error: toastErrorMock,
    info: toastInfoMock,
    success: vi.fn(),
    warning: vi.fn(),
  },
}));

// chat.workflowId is the chat's main thread id — the mailbox the composer
// queues into, and therefore the one the strip must read.
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<
    typeof import("../../../hooks/chat-queries")
  >("../../../hooks/chat-queries");
  return {
    ...actual,
    useChat: () => ({
      data: { id: CHAT_ID, workflowId: MAIN_THREAD_ID },
      isLoading: false,
    }),
    getChatFromCache: () => undefined,
  };
});

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingQuestion: () => ({ data: null, isLoading: false }),
}));

const NO_PRESETS = { presets: [] as never[], loading: false };
const NO_MODELS = { models: [] as never[] };
const NO_SLASH_COMMANDS: never[] = [];

vi.mock("../../../store/globalDataStore", () => ({
  usePresetsForWorkflow: () => NO_PRESETS,
  useModels: () => NO_MODELS,
  useGlobalDataStore: Object.assign(() => undefined, {
    getState: () => ({ models: [] }),
  }),
}));

vi.mock("../../Settings/ModelPreferences", () => ({
  loadTagModelConfigs: vi.fn().mockResolvedValue({}),
}));

vi.mock("../../../hooks/useSlashCommands", () => ({
  useSlashCommands: () => NO_SLASH_COMMANDS,
}));

vi.mock("../../../api/workflow-grpc", () => ({
  workflowGrpc: { getWorkflow: vi.fn().mockResolvedValue({ workflow: null }) },
}));

vi.mock("../../../api/preset-grpc", () => ({
  presetGrpc: {
    getDefaultPresets: vi.fn().mockResolvedValue({}),
    listPresets: vi.fn().mockResolvedValue([]),
  },
}));

vi.mock("../settings", () => ({
  ChatSettingsPopover: () => null,
}));
vi.mock("../WorkflowSelector", () => ({
  WorkflowSelector: () => null,
}));

import { ChatInput } from "../ChatInput";

const HUMAN = 5;

function queuedRow(body: string, id = "queued-1") {
  return {
    id,
    body,
    created_at: new Date(Date.now() - 3_000).toISOString(),
    sender_kind: HUMAN,
    attachments: [],
  };
}

function typeInComposer(text: string) {
  const editor = screen.getByTestId("chat-input");
  editor.textContent = text;
  fireEvent.input(editor);
  return editor;
}

describe("the queued-message strip inside the composer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAttachmentStore.getState().reset();
    sendAgentMessageMock.mockResolvedValue({ success: true, message: "" });
    listQueuedAgentMessagesMock.mockResolvedValue({ messages: [] });
    cancelQueuedAgentMessageMock.mockResolvedValue({ success: true, message: "Cancelled" });
  });

  it("reads the mailbox of the thread the composer queues into", async () => {
    listQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queuedRow("already waiting")],
    });

    renderWithQuery(
      <ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={true} />,
    );

    await waitFor(() => {
      expect(screen.getByText("already waiting")).toBeTruthy();
    });
    expect(listQueuedAgentMessagesMock).toHaveBeenCalledWith(CHAT_ID, MAIN_THREAD_ID);
  });

  it("shows a just-queued message without waiting for the next poll", async () => {
    renderWithQuery(
      <ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={true} />,
    );

    // Nothing queued yet, so the strip is absent — not an empty box.
    await waitFor(() => {
      expect(listQueuedAgentMessagesMock).toHaveBeenCalled();
    });
    expect(screen.queryByTestId("queued-messages")).toBeNull();

    // The next read reflects the message about to be queued.
    listQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queuedRow("check the migration file")],
    });

    const editor = typeInComposer("check the migration file");
    fireEvent.keyDown(editor, { key: "Enter" });

    await waitFor(() => {
      expect(sendAgentMessageMock).toHaveBeenCalledTimes(1);
    });
    // The refresh is driven by the queue succeeding, not by the 2.5s poll —
    // this resolves well inside a single interval.
    await waitFor(() => {
      expect(screen.getByText("check the migration file")).toBeTruthy();
    });
  });

  it("does not poll the mailbox while the agent is idle", async () => {
    renderWithQuery(
      <ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={false} />,
    );

    // One read on mount is fine — it is the repeated polling of a mailbox
    // nothing will ever drain that would be waste.
    await waitFor(() => {
      expect(listQueuedAgentMessagesMock).toHaveBeenCalled();
    });
    const callsAfterMount = listQueuedAgentMessagesMock.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(listQueuedAgentMessagesMock.mock.calls.length).toBe(callsAfterMount);
  });
});
