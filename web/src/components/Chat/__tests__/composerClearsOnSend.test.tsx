/**
 * The composer must empty itself when a message is sent.
 *
 * The interesting cases are the ones where `onSend` does not resolve
 * immediately: the send RPC takes a moment, or it fails outright. In both the
 * user has already committed the message, so the text must not sit in the box
 * looking unsent.
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

const { sendAgentMessageMock, toastErrorMock, toastInfoMock } = vi.hoisted(() => ({
  sendAgentMessageMock: vi.fn(),
  toastErrorMock: vi.fn(),
  toastInfoMock: vi.fn(),
}));

vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: { sendAgentMessage: sendAgentMessageMock },
}));

vi.mock("../../../lib/toast-manager", () => ({
  toast: {
    error: toastErrorMock,
    info: toastInfoMock,
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
