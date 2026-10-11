import { Code, ConnectError } from "@connectrpc/connect";
import type { WorkflowState } from "../gen/reliant/v1/chat_pb";
import { chatNeedsStart } from "./workflowLifecycle";

export interface ExistingChatSendOptions {
  workflow: string | null;
  workflowParams?: Record<string, unknown>;
  targetThread?: string | null;
  selectedPresets?: Record<string, string>;
  // The id this send keeps on screen (chatStore.sendMessage). One id for every
  // attempt, including a fallback to StartChat, so they are one message.
  clientMessageId?: string;
}

export interface ExistingChatSendActions {
  startExistingChat: (
    chatId: string,
    content: string,
    attachmentIds: string[] | undefined,
    options: Omit<ExistingChatSendOptions, "targetThread">,
  ) => Promise<unknown>;
  sendMessage: (
    chatId: string,
    content: string,
    attachmentIds: string[] | undefined,
    options: ExistingChatSendOptions,
  ) => Promise<unknown>;
}

// The server's refusal of SendMessage on a chat that has not started
// (internal/grpc/services/chat_send.go). It carries no typed detail, so the
// message is matched, but only together with the FailedPrecondition code.
const NOT_STARTED_MESSAGE = "chat has not started";

function isNotStartedError(error: unknown): boolean {
  const connectError = ConnectError.from(error);
  return (
    connectError.code === Code.FailedPrecondition &&
    connectError.rawMessage.includes(NOT_STARTED_MESSAGE)
  );
}

/**
 * Routes a send on an existing chat. A chat whose root run is PENDING (a
 * branch's first send) must go through StartChat; the server rejects
 * SendMessage on it. Everything else — active, paused, finished — is SendMessage.
 */
export async function sendOnExistingChat(
  chat: { workflowState: WorkflowState } | null | undefined,
  chatId: string,
  content: string,
  attachmentIds: string[] | undefined,
  options: ExistingChatSendOptions,
  actions: ExistingChatSendActions,
): Promise<void> {
  if (chatNeedsStart(chat)) {
    const { targetThread: _targetThread, ...startOptions } = options;
    await actions.startExistingChat(chatId, content, attachmentIds, startOptions);
    return;
  }
  try {
    await actions.sendMessage(chatId, content, attachmentIds, options);
  } catch (error) {
    // Defence in depth: the cached chat said "started" but the server says it
    // has not (a stale detail entry). Start it once instead of failing the send.
    if (!isNotStartedError(error)) throw error;
    const { targetThread: _targetThread, ...startOptions } = options;
    await actions.startExistingChat(chatId, content, attachmentIds, startOptions);
  }
}
