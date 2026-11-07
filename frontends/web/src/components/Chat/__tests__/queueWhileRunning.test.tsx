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

import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
    // The composer reads the chat's server-side selection on mount (a
    // GetChat). Unmocked, that request reached the network guard only
    // sometimes, failing a different case each time (reliant#2366).
    useChatSelection: () => ({ data: undefined, isLoading: false }),
  };
});

vi.mock("../../../hooks/question-queries", () => ({
  usePendingQuestions: () => ({ data: { questions: [] }, isLoading: false }),
}));

// These results feed effect dependency arrays that call setState. Fresh array
// identities on every render would spin the component forever, so the mocks
// hand back the same frozen objects each time.
const NO_PRESETS = { presets: [] as never[], loading: false };
const NO_MODELS = { models: [] as never[] };
const NO_SLASH_COMMANDS: never[] = [];

vi.mock("../../../store/globalDataStore", () => ({
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

// The selected workflow has no inputs. One stable result, as React Query
// returns one: a fresh object per render re-derives the params every render.
vi.mock("../../../hooks/workflow-queries", async (importActual) => {
  const NO_DEFINITION = { data: { workflow: undefined, version: 0, status: "complete", validationErrors: [] } };
  return {
    ...(await importActual<typeof import("../../../hooks/workflow-queries")>()),
    fetchWorkflow: vi.fn().mockResolvedValue(NO_DEFINITION.data),
    useWorkflowDefinitionQuery: () => NO_DEFINITION,
  };
});

vi.mock("../../../hooks/preset-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/preset-queries")>()),
  usePresetsForWorkflow: () => NO_PRESETS,
  fetchDefaultPresets: vi.fn().mockResolvedValue({}),
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
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { emptyChatPullReads } from "@/test/chatPullReads";

// ChatService's pull reads are generated-key reads, so they are answered at
// the transport.
beforeEach(() => setTransport(mockTransport({ ...emptyChatPullReads(), "ChatService.SendAgentMessage": (req: unknown) => sendAgentMessageMock(req) })));
afterEach(() => setTransport(null));

function typeInComposer(text: string) {
  const editor = screen.getByTestId("chat-input");
  fireEvent.change(editor, { target: { value: text } });
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
    localStorage.clear();
    useAttachmentStore.getState().reset();
    sendAgentMessageMock.mockResolvedValue({ success: true, message: "" });
  });

  it.each([true, false])("keeps an attachment-only pasted queue send-owned through delayed settlement (%s)", async (success) => {
    localStorage.clear();
    const originalAttach = useAttachmentStore.getState().attachFile;
    const originalRemove = useAttachmentStore.getState().removeAttachment;
    const remove = vi.fn();
    const attachFile = vi.fn(async (session: string, file: File) => {
      const attachment = { id: "paste-queued", filename: file.name, mime_type: file.type, size: file.size, url: "", attachment_type: "file_reference" as const };
      useAttachmentStore.setState({ attachments: new Map([[session, [attachment]]]) as never });
      return attachment;
    });
    let finish!: (response: { success: boolean; message: string }) => void;
    sendAgentMessageMock.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    useAttachmentStore.setState({ attachFile, removeAttachment: remove });
    try {
      const view = renderWithQuery(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming />);
      const event = new Event("paste", { bubbles: true, cancelable: true });
      Object.defineProperty(event, "clipboardData", { value: { files: [], items: [], getData: () => "long text\n".repeat(600) } });
      await act(async () => { screen.getByTestId("chat-input").dispatchEvent(event); });
      expect(event.defaultPrevented).toBe(true);
      const queue = screen.getByTestId("queue-send-button");
      expect(queue).toBeEnabled();
      fireEvent.click(queue);
      await waitFor(() => expect(sendAgentMessageMock).toHaveBeenCalledTimes(1));
      expect(sendAgentMessageMock).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringMatching(/^Attached: Pasted text /), attachments: ["paste-queued"] }));
      expect(screen.getByText("Edit").closest("button")).toBeDisabled();
      expect(screen.getByText("Remove").closest("button")).toBeDisabled();
      // Submitted pastes must not reappear as ordinary removable attachments.
      expect(screen.queryByRole("button", { name: /^Remove Pasted text / })).toBeNull();
      for (const button of screen.getAllByRole("button").filter((button) => /remove/i.test(button.getAttribute("aria-label") ?? button.textContent ?? ""))) {
        if (!button.matches(":disabled")) fireEvent.click(button);
      }
      expect(remove).not.toHaveBeenCalled();
      view.unmount();
      expect(remove).not.toHaveBeenCalled();
      await act(async () => { finish({ success, message: success ? "" : "rejected" }); });
      expect(remove).not.toHaveBeenCalled();
      const stored = Object.keys(localStorage).filter((key) => key.startsWith("reliant:long-paste:"));
      expect(stored.length).toBe(success ? 0 : 1);
      if (!success) expect(localStorage.getItem(stored[0])).toContain("long text");
      expect(useAttachmentStore.getState().attachments.get(CHAT_ID) ?? []).toEqual([]);
    } finally { useAttachmentStore.setState({ attachFile: originalAttach, removeAttachment: originalRemove }); }
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

    await waitFor(() => expect(sendAgentMessageMock).toHaveBeenCalledTimes(1));
    expect(sendAgentMessageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        chatId: CHAT_ID,
        threadId: MAIN_THREAD_ID,
        message: "check the migration file",
        attachments: [],
        clientMessageId: expect.any(String),
      }),
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

    await waitFor(() => expect(sendAgentMessageMock).toHaveBeenCalledTimes(1));
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(sendAgentMessageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        chatId: CHAT_ID,
        threadId: MAIN_THREAD_ID,
        message: "look at this",
        attachments: ["att-1"],
        clientMessageId: expect.any(String),
      }),
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
