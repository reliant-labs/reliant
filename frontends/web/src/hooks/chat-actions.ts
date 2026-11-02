// Copyright (c) 2025 Reliant Labs

/**
 * ChatService and ToolCallService calls the chat core makes by hand: every
 * write the stream answers (start, send, branch, pause, …) and the reads whose
 * answer the stream then maintains in place (the chat list, a chat, its
 * messages). They stay off the generated hooks for the reason in
 * research/forge-migration/16-p9-r4-wave-d-plan.md §2.2: every generated
 * ChatService mutation invalidates `chatServiceKeys.all`, and the chat core's
 * caches (`chatKeys`, `messageKeys`) are written by the stream and must never
 * be refetched over. The cache work around each call lives with its caller
 * (chatStore, chat-queries, message-queries).
 *
 * Each function is the RPC through `connectClient`, with its request built
 * and its response converted by `chat-model.ts`. The pull reads (archived,
 * search, executions, thread inputs, a mailbox) are generated reads instead:
 * chat-pull-queries.ts and its neighbours.
 */

import { create } from "@bufbuild/protobuf";

import {
  ChatService,
  ChatState,
  MessageRole,
  ProviderPolicySchema,
  type ChatSelection,
  type ProviderPolicy_OnLimit,
} from "../gen/reliant/v1/chat_pb";
import type { WorktreeSummary } from "../gen/reliant/v1/worktree_pb";
import { ToolCallService } from "../gen/reliant/v1/tool_call_pb";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import type { Chat, Message } from "../types/chat";
import type { BranchChatRequest, StartChatRequest, UpdateChatRequest } from "../types/api";
import {
  buildWorkflowParamsPayload,
  convertProtoChat,
  convertProtoMessage,
  replayEventToProto,
  type RunNowOptions,
  type UpdateChatOptions,
} from "./chat-model";

const client = connectClient(ChatService);
const toolCalls = connectClient(ToolCallService);

function chatOf<R extends { chat?: Parameters<typeof convertProtoChat>[0] }>(response: R): Chat {
  if (!response.chat) throw new Error("No chat in response");
  return convertProtoChat(response.chat);
}

// ── Chats ───────────────────────────────────────────────────────────────────

/**
 * A project's non-archived chats, as the sidebar lists them, with the user
 * update sequence the stream resumes from and the project's main workspace.
 * No project lists nothing.
 */
/**
 * The sidebar's chat list: one project's chats, or every project's when
 * `projectId` is undefined (the "All projects" view). `mainWorktrees` carries
 * the main workspace of each project in that scope.
 *
 * Pass the query's `signal`: a refetch that supersedes this one aborts it, and
 * the abort cancels the server's read too. Without it a cancelled fetch kept
 * running to completion on the server, so a burst of invalidations piled up
 * that many concurrent ListChats against the database.
 */
export async function listChats(
  projectId?: string,
  options: { signal?: AbortSignal } = {},
): Promise<{
  chats: Chat[];
  lastUserUpdateSequence: number;
  mainWorktrees: WorktreeSummary[];
}> {
  const response = await client.listChats({
    projectId: projectId ?? "",
    // ListChats is the chat list: the server returns only conversations
    // (USER-origin chats and runs the user adopted). Runs an automation,
    // an agent or the CLI started live in Runs; one waiting on the user
    // surfaces in the Inbox. Filtering server-side is what stops a busy
    // automation from filling the page and pushing real conversations out
    // of it. A run is still reachable by id (GetChat) — see resolveChat in
    // hooks/chat-queries.ts.
    // Across every project a page has to hold more than one project's worth.
    ...(projectId ? {} : { limit: ALL_PROJECTS_CHAT_LIMIT }),
  }, { signal: options.signal });
  return {
    chats: response.chats.map(convertProtoChat),
    lastUserUpdateSequence: Number(response.lastUserUpdateSequence),
    mainWorktrees: response.mainWorktrees,
  };
}

/** How many chats the all-projects list asks for: the most recently active. */
export const ALL_PROJECTS_CHAT_LIMIT = 300;

export async function getChat(chatId: string): Promise<Chat> {
  return chatOf(await client.getChat({ chatId }));
}

/**
 * First send: creates a new chat, or starts the existing PENDING chat named by
 * `chat_id`. `builder_test` records a workflow builder's test run (launch kind
 * builder.test; the workflow must be a saved draft the caller owns).
 */
export async function startChat(request: StartChatRequest & { builder_test?: boolean }): Promise<Chat> {
  const response = await client.startChat({
    chatId: request.chat_id,
    projectId: request.project_id,
    messages: request.messages.map((m) => ({ role: m.role, content: m.content, displayStyle: m.display_style })),
    title: request.title,
    worktreeId: request.worktree_id,
    daemonId: request.daemon_id || undefined,
    noMachine: request.no_machine || undefined,
    workflow: request.workflow,
    attachments: request.attachments || [],
    workflowParams: buildWorkflowParamsPayload(request.workflow_params),
    selectedPresets: request.selectedPresets || {},
    builderTest: request.builder_test || undefined,
    clientMessageId: request.client_message_id,
  });
  return chatOf(response);
}

/**
 * Run a workflow by hand ("Run now"): an attended start recorded as
 * manual.run. Works for automation-only workflows, and can replay a past
 * trigger event.
 */
export async function runNow(options: RunNowOptions): Promise<{ chat: Chat; workflow_id: string; run_id: string }> {
  const response = await client.runNow({
    projectId: options.projectId,
    workflow: options.workflow,
    message: options.message ?? "",
    inputs: buildWorkflowParamsPayload(options.inputs),
    presets: options.presets ?? {},
    worktreeId: options.worktreeId || undefined,
    daemonId: options.daemonId || undefined,
    noMachine: options.noMachine ?? false,
    replay: options.replay ? replayEventToProto(options.replay) : undefined,
    clientMessageId: options.clientMessageId,
  });
  return { chat: chatOf(response), workflow_id: response.workflowId, run_id: response.runId };
}

/**
 * Update chat metadata. Model/temperature/max_tokens are workflow params
 * (updateWorkflowParams); `workflow_name` changes only while the chat's root
 * workflow is pending (the chat has not started).
 */
export async function updateChat(chatId: string, updates: UpdateChatOptions | UpdateChatRequest): Promise<Chat> {
  const response = await client.updateChat({
    chatId,
    title: updates.title,
    worktreeId: updates.worktree_id,
    workflowName: "workflow_name" in updates ? updates.workflow_name : undefined,
  });
  return chatOf(response);
}

export async function deleteChat(chatId: string): Promise<{
  success: boolean;
  message: string;
  permanently_deleted: boolean;
}> {
  const response = await client.deleteChat({ chatId });
  return {
    success: response.success,
    message: response.message,
    permanently_deleted: response.permanentlyDeleted,
  };
}

async function updateChatState(chatId: string, state: ChatState) {
  return client.updateChatState({ chatId, state });
}

export async function archiveChat(chatId: string): Promise<{ message: string }> {
  const response = await updateChatState(chatId, ChatState.ARCHIVED);
  return { message: response.message };
}

/** Restore an archived chat. The server restores its workspace with it. */
export async function unarchiveChat(chatId: string): Promise<{ message: string; worktree_restored: boolean }> {
  const response = await updateChatState(chatId, ChatState.IDLE);
  return { message: response.message, worktree_restored: false };
}

/** Terminate the running workflow. */
export async function cancelChat(chatId: string): Promise<{ status: string; message: string; chat_id: string }> {
  const response = await client.terminateChat({ chatId });
  return { status: response.success ? "cancelled" : "failed", message: response.message, chat_id: chatId };
}

export async function pauseChat(chatId: string): Promise<{ success: boolean; message: string }> {
  const response = await client.pauseChat({ chatId });
  return { success: response.success, message: response.message };
}

/** Resume a paused or expired workflow. */
export async function resumeChat(chatId: string): Promise<{
  success: boolean;
  message: string;
  workflow_id: string;
  run_id: string;
  needs_recovery: boolean;
  recovery_type: number;
}> {
  const response = await client.resumeChat({ chatId });
  return {
    success: response.success,
    message: response.message,
    workflow_id: response.workflowId,
    run_id: response.runId,
    needs_recovery: response.needsRecovery,
    recovery_type: response.recoveryType,
  };
}

/**
 * "Retry now" on a run held until a provider's usage limit resets. Resolves to
 * whether a waiting run was woken. `useModel` ("<model>@<driver>") answers the
 * wait with a cross-provider fallback instead, until the original reset.
 */
export async function retryUsageLimitWait(chatId: string, useModel?: string): Promise<boolean> {
  const response = await client.retryUsageLimitWait({ chatId, useModel });
  return response.woken;
}

/** Dismiss a chat's unread state. */
export async function dismissChat(chatId: string): Promise<{ state: ChatState; changed: boolean; message: string }> {
  const response = await client.dismissChat({ chatId });
  return { state: response.state, changed: response.changed, message: response.message };
}

export async function markChatUnread(chatId: string): Promise<{ state: ChatState; changed: boolean; message: string }> {
  const response = await client.markUnreadChat({ chatId });
  return { state: response.state, changed: response.changed, message: response.message };
}

/** Trigger manual context compaction of a thread. */
export async function compactChat(chatId: string, threadId: string): Promise<{
  success: boolean;
  workflow_id: string;
  run_id: string;
  message: string;
}> {
  const response = await client.compactChat({ chatId, threadId });
  // CompactChatResponse has no success or message: an answer is a success.
  return { success: true, workflow_id: response.workflowId, run_id: response.runId, message: "" };
}

/**
 * Branch a chat from one of its messages. `noMachine` is "Continue without
 * machine": the branch has no machine (no worktree, no daemon).
 */
export async function branchChat(
  chatId: string,
  request: BranchChatRequest & { worktreeId?: string; noMachine?: boolean },
): Promise<{ chat: Chat }> {
  if (!request.messageId) {
    throw new Error("messageId is required for branching");
  }
  const response = await client.branchChat({
    chatId,
    messageId: request.messageId,
    title: request.title,
    worktreeId: request.worktreeId,
    noMachine: request.noMachine || undefined,
  });
  return { chat: chatOf(response) };
}

/**
 * Pin a chat to a machine. On a chat with no machine this is "Connect a
 * machine": the server clears no_machine in the same write, and the next turn
 * gets the full tool set. An empty daemonId clears the pin.
 */
export async function setChatDaemon(chatId: string, daemonId: string): Promise<Chat> {
  return chatOf(await client.setChatDaemon({ chatId, daemonId }));
}

/**
 * Edit a chat's params. The server stores them as the chat's selection (the
 * choice every later run starts from) whether or not a run is live, and
 * signals a live run. `expectedVersion` is the selection version the edit was
 * made against: a stale one is refused with Code.Aborted and the caller
 * refetches. The response carries the selection as stored, after any
 * unservable-model move.
 */
export async function updateWorkflowParams(
  chatId: string,
  params: Record<string, unknown>,
  threadId?: string,
  expectedVersion?: bigint,
): Promise<{ success: boolean; chat_id: string; message: string; selection?: ChatSelection }> {
  const response = await client.updateWorkflowParams({
    chatId,
    params: buildWorkflowParamsPayload(params),
    threadId: threadId || undefined,
    expectedVersion,
  });
  return { success: response.success, chat_id: chatId, message: response.message, selection: response.selection };
}

/** Set what the chat does when its provider is rate-limited. */
export async function updateChatProviderPolicy(
  chatId: string,
  providerPolicy: { onLimit: ProviderPolicy_OnLimit; fallbackModel?: string },
  expectedVersion?: bigint,
): Promise<ChatSelection | undefined> {
  const response = await client.updateChatProviderPolicy({
    chatId,
    providerPolicy: create(ProviderPolicySchema, providerPolicy),
    expectedVersion,
  });
  return response.selection;
}

// ── Messages ────────────────────────────────────────────────────────────────

/** Send a user message and continue the chat's workflow. */
export async function sendMessage(
  chatId: string,
  content: string,
  attachments?: string[],
  options?: {
    workflow?: string | null;
    mode?: string;
    temperature?: number;
    max_tokens?: number;
    workflow_params?: Record<string, unknown>;
    target_thread?: string;
    selected_presets?: Record<string, string>;
    /** The id the client gave this message; a queued row is created with it. */
    client_message_id?: string;
  },
): Promise<{ chatId: string; workflowId: string; runId: string; messageId: string; queued: boolean }> {
  logger.info("[chat-actions.sendMessage] DEBUG options:", { workflow_params: options?.workflow_params });
  const response = await client.sendMessage({
    clientMessageId: options?.client_message_id,
    chatId,
    messages: content ? [{ role: MessageRole.USER, content }] : [],
    attachments: attachments || [],
    workflow: options?.workflow ?? undefined,
    mode: options?.mode,
    temperature: options?.temperature,
    maxTokens: options?.max_tokens ? BigInt(options.max_tokens) : undefined,
    workflowParams: buildWorkflowParamsPayload(options?.workflow_params),
    targetThread: options?.target_thread,
    selectedPresets: options?.selected_presets || {},
  });
  return {
    chatId: response.chatId,
    workflowId: response.workflowId,
    runId: response.runId,
    messageId: response.messageId,
    // The message was queued for the thread's next turn, not saved to
    // history: messageId is the queued row's id.
    queued: response.queued,
  };
}

/**
 * A chat's messages with their content blocks: the most recent `recent`,
 * those before `beforeSeq`, or exactly one thread (`threadId`, its inherited
 * history resolved).
 */
export async function listMessages(
  chatId: string,
  options?: { recent?: number; beforeSeq?: number; threadId?: string },
): Promise<{ messages: Message[]; total: number; hasMore: boolean; oldestSeq: number }> {
  const response = await client.listMessages({
    chatId,
    recent: options?.recent,
    beforeSeq: options?.beforeSeq ? BigInt(options.beforeSeq) : undefined,
    threadId: options?.threadId,
  });
  return {
    messages: response.messages.map(convertProtoMessage),
    total: response.total,
    hasMore: response.hasMore,
    oldestSeq: Number(response.oldestSeq),
  };
}

// ── A thread's mailbox ──────────────────────────────────────────────────────

/**
 * Queue a message directly into a running thread's mailbox, without pausing or
 * touching workflow/pause state: the human-facing counterpart to the
 * spawn_send tool, so a user can steer a running sub-agent without pausing the
 * whole chat.
 */
export async function sendAgentMessage(
  chatId: string,
  threadId: string,
  message: string,
  attachments?: string[],
  clientMessageId?: string,
): Promise<{ success: boolean; message: string }> {
  const response = await client.sendAgentMessage({
    chatId,
    threadId,
    message,
    attachments: attachments ?? [],
    clientMessageId,
  });
  return { success: response.success, message: response.message };
}

/**
 * Revoke a queued entry before the agent drains it. This races the agent's own
 * drain, so `success: false` is a legitimate outcome for a well-formed request:
 * the message was already delivered and the row still stands. Callers must
 * branch on it rather than assuming the cancel took.
 */
export async function cancelQueuedAgentMessage(
  chatId: string,
  messageId: string,
): Promise<{ success: boolean; message: string }> {
  const response = await client.cancelQueuedAgentMessage({ chatId, messageId });
  return { success: response.success, message: response.message };
}

/**
 * Stop what a thread is doing right now so it reads its mailbox at once,
 * instead of at the end of the work already in flight.
 *
 * Takes no message id, deliberately. Interrupt is an action on the THREAD: it
 * cancels the executing tool calls, and the next call_llm delivers whatever
 * the mailbox holds, oldest first. That is what makes "queue a message, then
 * decide to send it now" work without a race — there is no row to update, so
 * there is no window in which the update and the delivery can disagree. Queue
 * first (sendAgentMessage), then interrupt.
 */
export async function interruptThread(
  chatId: string,
  threadId: string,
): Promise<{ cancelledToolCalls: number; undeliverableToolCalls: string[] }> {
  const response = await client.interruptThread({ chatId, threadId });
  return {
    cancelledToolCalls: response.cancelledToolCalls,
    undeliverableToolCalls: response.undeliverableToolCalls,
  };
}

// ── Tool calls ──────────────────────────────────────────────────────────────

export async function cancelToolCall(toolCallId: string): Promise<{
  status: string;
  message: string;
  tool_call_id: string;
  session_id: string;
  chat_id: string;
}> {
  const response = await toolCalls.cancelToolCall({ toolCallId });
  return {
    status: response.success ? "success" : "error",
    message: response.message,
    tool_call_id: response.toolCallId,
    session_id: response.sessionId,
    chat_id: response.chatId,
  };
}

/** Move a running tool call to background execution. */
export async function convertToolCallToBackground(toolCallId: string): Promise<{
  status: string;
  message: string;
  process_id: string;
  tool_call_id: string;
  session_id: string;
  chat_id: string;
}> {
  const response = await toolCalls.convertToBackground({ toolCallId });
  return {
    status: response.success ? "success" : "error",
    message: response.message,
    process_id: response.processId,
    tool_call_id: response.toolCallId,
    session_id: response.sessionId,
    chat_id: response.chatId,
  };
}
