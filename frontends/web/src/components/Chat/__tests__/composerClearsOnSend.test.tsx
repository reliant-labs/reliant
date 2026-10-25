/**
 * The composer must empty itself when a message is sent.
 *
 * The interesting cases are the ones where `onSend` does not resolve
 * immediately: the send RPC takes a moment, or it fails outright. In both the
 * user has already committed the message, so the text must not sit in the box
 * looking unsent.
 */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { useAttachmentStore } from "../../../store/attachmentStore";

const CHAT_ID = "chat-abc";
const MAIN_THREAD_ID = "workflow-xyz";

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

vi.mock("../../../hooks/question-queries", () => ({
  usePendingQuestions: () => ({ data: { questions: [] }, isLoading: false }),
}));

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
  editor.textContent = text;
  fireEvent.input(editor);
  return editor;
}

function pressEnter(editor: HTMLElement): boolean {
  return fireEvent.keyDown(editor, {
    key: "Enter",
    shiftKey: false,
    metaKey: false,
    ctrlKey: false,
  });
}

describe("composer clears when a message is sent", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAttachmentStore.getState().reset();
  });

  it("clears after a send that resolves immediately", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    renderWithQuery(
      <ChatInput onSend={onSend} chatId={CHAT_ID} isStreaming={false} />
    );

    const editor = typeInComposer("hello there");
    pressEnter(editor);

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(editor.textContent).toBe(""));
  });

  it("clears immediately, without waiting for a slow send to resolve", async () => {
    let releaseSend: () => void = () => {};
    const sendGate = new Promise<void>((resolve) => {
      releaseSend = resolve;
    });
    const onSend = vi.fn().mockReturnValue(sendGate);

    renderWithQuery(
      <ChatInput onSend={onSend} chatId={CHAT_ID} isStreaming={false} />
    );

    const editor = typeInComposer("a slow message");
    pressEnter(editor);

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));

    // The RPC is still in flight. The user has committed the message, so the
    // box must already be empty and ready for the next one.
    await waitFor(() => expect(editor.textContent).toBe(""));

    releaseSend();
    await sendGate;
  });

  // ChatContainer toasts the failure and rethrows, so the composer must both
  // clear and absorb the rejection rather than leaving it unhandled.
  it("clears even when the send fails", async () => {
    const onSend = vi.fn().mockRejectedValue(new Error("network down"));

    renderWithQuery(
      <ChatInput onSend={onSend} chatId={CHAT_ID} isStreaming={false} />
    );

    const editor = typeInComposer("a doomed message");
    pressEnter(editor);

    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(editor.textContent).toBe(""));
  });
});
