import type { WorkflowState } from "../gen/reliant/v1/chat_pb";
import { chatNeedsStart } from "./workflowLifecycle";

export interface ExistingChatSendOptions {
  workflow: string | null;
  workflowParams?: Record<string, unknown>;
  targetThread?: string | null;
  selectedPresets?: Record<string, string>;
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
  await actions.sendMessage(chatId, content, attachmentIds, options);
}
