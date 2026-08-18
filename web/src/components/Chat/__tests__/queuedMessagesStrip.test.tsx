/**
 * The composer's half of the pending-queue wiring.
 *
 * The strip itself renders in ChatPresenter (above the background-work pill —
 * see ChatPresenter.attentionBand.test.tsx), not here. What the composer still
 * owns is the mailbox subscription that makes the strip correct:
 *
 *  - it subscribes to the mailbox of the thread it queues INTO, so both
 *    readers share one cache entry and the strip cannot end up displaying a
 *    different thread's queue than the one Enter writes to.
 *  - queueing refreshes that mailbox immediately, so a queued message appears
 *    without waiting up to a poll interval — the user's original complaint was
 *    queueing a message and seeing nothing.
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

describe("the composer's queued-mailbox subscription", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAttachmentStore.getState().reset();
    sendAgentMessageMock.mockResolvedValue({ success: true, message: "" });
    listQueuedAgentMessagesMock.mockResolvedValue({ messages: [] });
    cancelQueuedAgentMessageMock.mockResolvedValue({ success: true, message: "Cancelled" });
  });

  it("subscribes to the mailbox of the thread it queues into", async () => {
    listQueuedAgentMessagesMock.mockResolvedValue({
      messages: [queuedRow("already waiting")],
    });

    renderWithQuery(
      <ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={true} />,
    );

    // The mailbox read is keyed on chat + main thread — the same key the
    // presenter's strip reads, so the two share one cache entry rather than
    // showing different queues.
    await waitFor(() => {
      expect(listQueuedAgentMessagesMock).toHaveBeenCalledWith(
        CHAT_ID,
        MAIN_THREAD_ID,
      );
    });
  });

  it("re-reads the mailbox as soon as a message is queued, not on the next poll", async () => {
    renderWithQuery(
      <ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={true} />,
    );

    await waitFor(() => {
      expect(listQueuedAgentMessagesMock).toHaveBeenCalled();
    });
    const readsBeforeQueueing = listQueuedAgentMessagesMock.mock.calls.length;

    const editor = typeInComposer("check the migration file");
    fireEvent.keyDown(editor, { key: "Enter" });

    await waitFor(() => {
      expect(sendAgentMessageMock).toHaveBeenCalledTimes(1);
    });
    // Driven by the queue succeeding, not by the 2.5s poll — this resolves
    // well inside a single interval, which is what puts the message on screen
    // immediately instead of up to an interval later.
    await waitFor(() => {
      expect(listQueuedAgentMessagesMock.mock.calls.length).toBeGreaterThan(
        readsBeforeQueueing,
      );
    });
    expect(toastInfoMock).not.toHaveBeenCalled();
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
