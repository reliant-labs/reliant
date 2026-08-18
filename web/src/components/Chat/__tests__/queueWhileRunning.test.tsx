/**
 * Queueing a message to a running agent from the MAIN composer.
 *
 * While an agent is streaming, Enter no longer inserts a newline — it queues
 * the typed text into the running thread's mailbox via SendAgentMessage, which
 * the loop executor drains at its next step boundary. Three things pin that
 * behavior:
 *
 *  - streaming + text  -> sendAgentMessage(chatId, mainThreadId, text, []),
 *                         Enter is consumed (preventDefault), so no newline
 *                         lands.
 *  - streaming + files -> the attachment IDs ride along on the same queue
 *                         call, exactly like an ordinary send; the queue no
 *                         longer refuses them.
 *  - idle              -> untouched. Enter still runs the ordinary send path
 *                         and never reaches sendAgentMessage.
 */

import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useAttachmentStore } from "../../../store/attachmentStore";

const CHAT_ID = "chat-abc";
const MAIN_THREAD_ID = "workflow-xyz";

// jsdom ships no ResizeObserver; the composer uses one only to pick a compact
// layout, which no assertion here depends on.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const { sendAgentMessageMock, toastErrorMock } = vi.hoisted(() => ({
  sendAgentMessageMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: { sendAgentMessage: sendAgentMessageMock },
}));

vi.mock("../../../lib/toast-manager", () => ({
  toast: {
    error: toastErrorMock,
    info: vi.fn(),
    success: vi.fn(),
    warning: vi.fn(),
  },
}));

// The chat record is what supplies the main thread id (chat.workflowId ==
// Chat.MainThreadID() on the backend). Everything else about the chat is
// irrelevant to queueing.
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

// These results feed effect dependency arrays that call setState. Fresh array
// identities on every render would spin the component forever, so the mocks
// hand back the same frozen objects each time.
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

// Settings chrome pulls in model/preset pickers that have nothing to do with
// the composer's key handling.
vi.mock("../settings", () => ({
  ChatSettingsPopover: () => null,
}));
vi.mock("../WorkflowSelector", () => ({
  WorkflowSelector: () => null,
}));

import { ChatInput } from "../ChatInput";

function typeInComposer(text: string) {
  const editor = screen.getByTestId("chat-input");
  editor.textContent = text;
  fireEvent.input(editor);
  return editor;
}

/** Returns false when a handler called preventDefault on the Enter key. */
function pressEnter(editor: HTMLElement): boolean {
  return fireEvent.keyDown(editor, {
    key: "Enter",
    shiftKey: false,
    metaKey: false,
    ctrlKey: false,
  });
}

describe("queueing a message while the agent is running", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAttachmentStore.getState().reset();
    sendAgentMessageMock.mockResolvedValue({ success: true, message: "" });
  });

  it("queues to the main thread on Enter while streaming, and consumes the key", async () => {
    const onSend = vi.fn();
    renderWithQuery(
      <ChatInput onSend={onSend} chatId={CHAT_ID} isStreaming={true} />
    );

    const editor = typeInComposer("check the migration file");
    const notPrevented = pressEnter(editor);

    // preventDefault ran, so the browser never inserts the newline.
    expect(notPrevented).toBe(false);

    expect(sendAgentMessageMock).toHaveBeenCalledTimes(1);
    expect(sendAgentMessageMock).toHaveBeenCalledWith(
      CHAT_ID,
      MAIN_THREAD_ID,
      "check the migration file",
      []
    );
    // Queueing is not sending: the ordinary turn-starting path stays untouched.
    expect(onSend).not.toHaveBeenCalled();
  });

  it("queues attachments along with a message while streaming, instead of refusing", async () => {
    const onSend = vi.fn();
    const attachments = new Map();
    attachments.set(CHAT_ID, [
      {
        id: "att-1",
        filename: "diagram.png",
        size: 1024,
        mime_type: "image/png",
        url: "/attachments/att-1",
      },
    ]);
    useAttachmentStore.setState({ attachments });

    renderWithQuery(
      <ChatInput onSend={onSend} chatId={CHAT_ID} isStreaming={true} />
    );

    const editor = typeInComposer("look at this");
    pressEnter(editor);

    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(sendAgentMessageMock).toHaveBeenCalledTimes(1);
    expect(sendAgentMessageMock).toHaveBeenCalledWith(
      CHAT_ID,
      MAIN_THREAD_ID,
      "look at this",
      ["att-1"]
    );
  });

  it("leaves the idle Enter path alone", async () => {
    const onSend = vi.fn();
    renderWithQuery(
      <ChatInput onSend={onSend} chatId={CHAT_ID} isStreaming={false} />
    );

    const editor = typeInComposer("start a new turn");
    pressEnter(editor);

    expect(sendAgentMessageMock).not.toHaveBeenCalled();
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend).toHaveBeenCalledWith(
      "start a new turn",
      undefined,
      expect.anything(),
      undefined,
      undefined,
      undefined
    );
  });
});
