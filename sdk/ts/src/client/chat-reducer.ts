// reduceChat: the pure fold of a chat's stream onto its state.
//
// (state, action) -> state, no clock, no store, no I/O. It carries the rules
// the stream protocol needs and a UI should not re-derive: a snapshot replaces
// and a replay merges; ephemeral deltas build a placeholder under the
// pre-allocated message id until the persisted message (or a finalize marker)
// retires it; a delta naming a finalized id is a stale tail and is dropped; a
// stream that stopped settles its unfinished tools to "cancelled"; tool
// statuses never regress; results join their calls by id, first write wins.

import { ContentBlockType, MessageRole, StreamingState } from "#gen/reliant/v1/chat_pb.js";
import type { ChatAction, ChatState, ReduceEnv } from "./chat-state.js";
import type {
  ActiveThreadUpdate,
  ChatUpdate,
  ErrorUpdate,
  InfoUpdate,
  NodeExecutionUpdate,
  ProtoMessageUpdate,
  QuestionUpdate,
  RunOutputUpdate,
  StreamFinalizedUpdate,
  StreamingDelta,
  ToolApprovalUpdate,
  ToolCallUpdate,
  WorkflowStatusUpdate,
} from "./events.js";
import { compareMessagesWithinThread } from "./message-order.js";
import {
  applyHistoricalErrors,
  applyLogSlice,
  applyToolCallStateUpdates,
  bySequenceNumber,
  isAbortedFinalizeReason,
  isMessageComplete,
  mergeActiveThreads,
  mergeMessages,
  normalizeThreadKey,
  oldestSeqOf,
  resolveStreamingBase,
  settleCancelledStreamBlocks,
  snapshotReplacesMessages,
  toolStatusSurvivesStreamAbort,
} from "./reducers.js";
import { failedSendId, optimisticSendId } from "./send-ids.js";
import {
  foldToolResultImages,
  foldToolResultMessage,
  resolvedToolResultFromBlock,
  type ToolResultsByCallId,
} from "./tool-results.js";
import type { ContentBlock, Message, ToolCallState, ToolExecutionStateUpdate } from "./types.js";

/** Sorts after every persisted message (seq of the optimistic echo of a send). */
const OPTIMISTIC_SEQ = 999998n;
/** Seq of a streaming placeholder with no persisted message to take it from. */
const PLACEHOLDER_SEQ = 999999n;

type StreamBlock = ContentBlock & { status?: string };

export function createChatState(
  chatId: string,
  init: { cursor?: bigint; syncPending?: boolean } = {},
): ChatState {
  return {
    chatId,
    cursor: init.cursor ?? 0n,
    syncPending: init.syncPending ?? true,
    messages: [],
    toolResults: {},
    toolCalls: new Map(),
    streaming: {},
    finalizedStreamIds: new Set(),
    approvals: [],
    threads: [],
    pendingQuestions: [],
    asyncQuestions: [],
    errors: [],
    info: [],
    runOutputs: [],
    nodeExecutions: [],
    workflowStatus: {},
    metadata: null,
    pagination: null,
    contextUsage: null,
  };
}

const defaultNow = (): string => new Date(0).toISOString();

export function reduceChat(state: ChatState, action: ChatAction, env: ReduceEnv = {}): ChatState {
  const now = env.now ?? defaultNow;
  switch (action.kind) {
    case "snapshot":
      return { ...applyUpdates(state, action.updates, true, now), cursor: action.cursor };
    case "updates":
      return { ...applyUpdates(state, action.updates, false, now), cursor: action.cursor };
    case "pagination":
      return { ...state, pagination: action.pagination };
    case "contextUsage":
      return { ...state, contextUsage: action.contextUsage };
    case "olderPage": {
      const held = new Set(state.messages.map((m) => m.id));
      const additions = action.messages.filter((m) => !held.has(m.id));
      // First write wins per call id, as for live results: this page's copy of
      // a result the chat already holds is a re-delivery.
      const pageResults: ToolResultsByCallId = {};
      for (const message of additions) {
        if (message.role === MessageRole.TOOL) foldToolResultMessage(message, pageResults);
      }
      return {
        ...state,
        messages: [...additions, ...state.messages].sort(compareMessagesWithinThread),
        toolResults: { ...state.toolResults, ...pageResults },
        // The page's own errors arrive with the history they belong to; merged
        // by id, never replacing a newer attempt the stream already delivered.
        errors: action.errors.length > 0 ? applyHistoricalErrors(state.errors, action.errors) : state.errors,
        pagination: {
          total: action.total,
          hasMore: action.hasMore,
          oldestSeq: oldestSeqOf([...additions, ...state.messages]),
        },
      };
    }
    case "caughtUp":
      return { ...state, syncPending: false, cursor: action.cursor > state.cursor ? action.cursor : state.cursor };
    case "sendStarted":
      return {
        ...state,
        messages: [
          ...state.messages.filter((m) => m.id !== optimisticSendId(action.clientMessageId)),
          optimisticMessage(state.chatId, optimisticSendId(action.clientMessageId), action.text, action.createdAt),
        ],
      };
    case "sendFailed":
      return {
        ...state,
        messages: state.messages.map((m) =>
          m.id === optimisticSendId(action.clientMessageId) ? { ...m, id: failedSendId(action.clientMessageId) } : m,
        ),
      };
    case "sendDiscarded":
      return {
        ...state,
        messages: state.messages.filter(
          (m) =>
            m.id !== failedSendId(action.clientMessageId) && m.id !== optimisticSendId(action.clientMessageId),
        ),
      };
  }
}

function optimisticMessage(chatId: string, id: string, text: string, createdAt: string): Message {
  return {
    id,
    chatId,
    role: MessageRole.USER,
    contentBlocks: [{ id: `${id}-b0`, type: ContentBlockType.TEXT, index: 0, content: text } as ContentBlock],
    createdAt,
    updatedAt: createdAt,
    streamingState: StreamingState.COMPLETE,
    seq: OPTIMISTIC_SEQ,
    thread: "",
    sequenceNumber: 0n,
    attachments: [],
  } as Message;
}

// ── Applying a batch ────────────────────────────────────────────────────────

function applyUpdates(
  state: ChatState,
  updates: ChatUpdate[],
  isSnapshot: boolean,
  now: () => string,
): ChatState {
  const chatId = state.chatId;
  const of = <T extends ChatUpdate>(type: T["update_type"]): T[] =>
    updates.filter((u): u is T => u.update_type === type);

  // Messages of another chat never belong here (a stale frame from a chat the
  // stream has since left).
  const messages: Message[] = (of<ProtoMessageUpdate>("message") as ProtoMessageUpdate[])
    .map((u) => u.message as unknown as Message)
    .filter((m) => !m.chatId || m.chatId === chatId);

  const rawDeltas = of<StreamingDelta>("streaming_delta");
  const finalized = of<StreamFinalizedUpdate>("stream_finalized");

  // Ids finalized by THIS batch's markers, and the subset whose stream STOPPED
  // (cancelled/aborted): no message row will ever replace such a placeholder.
  const batchFinalizedIds = new Set<string>();
  const batchAbortedIds = new Set<string>();
  const abortedThreadKeys = new Set<string>();
  for (const marker of finalized) {
    if (marker.message_id) batchFinalizedIds.add(marker.message_id);
    if (!isAbortedFinalizeReason(marker.reason)) continue;
    if (marker.message_id) batchAbortedIds.add(marker.message_id);
    abortedThreadKeys.add(normalizeThreadKey(chatId, marker.thread));
  }

  // THE DROP RULE: a delta stamped with a finalized id is a stale tail. Drop it
  // before it can fabricate a placeholder. Id-less deltas (old servers) pass.
  const isFinalizedId = (id: string | undefined): boolean => {
    if (!id) return false;
    if (state.finalizedStreamIds.has(id) || batchFinalizedIds.has(id)) return true;
    return state.messages.some(
      (m) => m.id === id && m.role === MessageRole.ASSISTANT && isMessageComplete(m),
    );
  };
  const liveDeltas = rawDeltas.filter((d) => !isFinalizedId(d.message_id));

  // Tool statuses: the persisted channel, plus tools cancelled mid-stream.
  const toolExecutionUpdates: ToolExecutionStateUpdate[] = of<ToolCallUpdate>("tool_call").map((tc) => ({
    update_type: "tool_execution_state",
    id: tc.tool_call_id,
    chat_id: chatId,
    tool_call_id: tc.tool_call_id,
    tool_name: tc.tool_name,
    status: tc.status,
    node_id: tc.node_id || "",
    sequence_number: tc.sequence_number || 0,
    timestamp: tc.requested_at || tc.started_at || tc.completed_at || now(),
    child_workflow_id: tc.child_workflow_id,
  }));
  const cancelledToolUpdates: ToolExecutionStateUpdate[] = liveDeltas
    .filter((d) => d.delta_type === "tool_cancelled" && d.tool_call)
    .map((d) => ({
      update_type: "tool_execution_state",
      id: d.tool_call!.id,
      chat_id: chatId,
      tool_call_id: d.tool_call!.id,
      tool_name: d.tool_call!.name,
      status: "cancelled",
      node_id: "",
      sequence_number: d.sequence_number || 0,
      timestamp: now(),
    }));
  const toolCallsBeforeAbort = applyToolCallStateUpdates(
    state.toolCalls as Map<string, ToolCallState>,
    [...toolExecutionUpdates, ...cancelledToolUpdates],
    chatId,
  );

  // A snapshot is the server's whole account of the chat: what it omits is
  // over. A placeholder is built from ephemeral deltas no snapshot carries, so
  // none survives one (a missed finalize would otherwise leave it on screen),
  // and the gates and workflow state the snapshot reports replace the held ones.
  const priorStreaming: Readonly<Record<string, Message>> = isSnapshot ? {} : state.streaming;
  const prior: Pick<ChatState, "approvals" | "threads" | "pendingQuestions" | "asyncQuestions" | "workflowStatus"> =
    isSnapshot
      ? { approvals: [], threads: [], pendingQuestions: [], asyncQuestions: [], workflowStatus: {} }
      : state;

  // Build each thread's placeholder from the deltas that survived.
  const processedStreaming = new Map<string, Message>();
  const deltasByThread = new Map<string | undefined, StreamingDelta[]>();
  for (const delta of liveDeltas) {
    const bucket = deltasByThread.get(delta.thread);
    if (bucket) bucket.push(delta);
    else deltasByThread.set(delta.thread, [delta]);
  }
  for (const [thread, threadDeltas] of deltasByThread) {
    const threadKey = normalizeThreadKey(chatId, thread);
    const base = resolveStreamingBase(priorStreaming as Record<string, Message | null>, threadKey);
    const built = foldStreamingDeltas(threadDeltas, base, chatId, toolCallsBeforeAbort, now());
    if (built) processedStreaming.set(threadKey, built);
  }

  // Tool results arrive as their own TOOL messages, often after the call.
  // tool_call_results.tool_call_id is a primary key at rest, so a second copy
  // is the same result re-delivered: first write wins.
  const batchResults: ToolResultsByCallId = {};
  const sourceMsgId: Record<string, string> = {};
  for (const msg of messages) {
    if (msg.role !== MessageRole.TOOL) continue;
    for (const block of msg.contentBlocks) {
      if (block.type === ContentBlockType.TOOL_RESULT && block.toolCallId) {
        if (sourceMsgId[block.toolCallId] !== undefined) continue;
        sourceMsgId[block.toolCallId] = msg.id;
        batchResults[block.toolCallId] = resolvedToolResultFromBlock(block);
      }
    }
    foldToolResultImages(msg, batchResults, sourceMsgId);
  }

  const completedThreads = new Set<string>();
  const newlyFinalizedIds = new Set<string>(batchFinalizedIds);
  for (const m of messages) {
    if (m.role === MessageRole.ASSISTANT && isMessageComplete(m)) {
      completedThreads.add(normalizeThreadKey(chatId, m.thread));
      if (m.id) newlyFinalizedIds.add(m.id);
    }
  }

  // A snapshot that would destroy already-loaded older pages is merged instead.
  const snapshotReplaces = isSnapshot && snapshotReplacesMessages(state.messages, messages);
  let nextMessages = mergeMessages(state.messages, messages, isSnapshot);

  const existingResults = snapshotReplaces ? {} : state.toolResults;
  for (const id of Object.keys(batchResults)) {
    if (id in existingResults) delete batchResults[id];
  }
  const toolResults: ToolResultsByCallId =
    Object.keys(batchResults).length === 0 ? existingResults : { ...existingResults, ...batchResults };

  const streaming = new Map<string, Message>(Object.entries(priorStreaming));
  for (const [key, msg] of processedStreaming) streaming.set(key, msg);

  // A complete message retires its thread's placeholder. Tool calls the
  // placeholder saw cancelled rejoin the message they streamed under, by tool
  // call id (a streaming block has no block id yet).
  for (const threadKey of completedThreads) {
    const placeholder = streaming.get(threadKey);
    if (placeholder) {
      const cancelled = (placeholder.contentBlocks as StreamBlock[]).filter(
        (b) => b.type === ContentBlockType.TOOL_CALL && b.status === "cancelled",
      );
      if (cancelled.length > 0) {
        nextMessages = nextMessages.map((msg) => {
          if (
            msg.role !== MessageRole.ASSISTANT ||
            !isMessageComplete(msg) ||
            normalizeThreadKey(chatId, msg.thread) !== threadKey ||
            msg.id !== placeholder.id
          ) {
            return msg;
          }
          const have = new Set(
            msg.contentBlocks
              .filter((b) => b.type === ContentBlockType.TOOL_CALL)
              .map((b) => b.toolCallId)
              .filter((id): id is string => !!id),
          );
          const missing = cancelled.filter((b) => !!b.toolCallId && !have.has(b.toolCallId));
          return missing.length > 0 ? { ...msg, contentBlocks: [...msg.contentBlocks, ...missing] } : msg;
        });
      }
    }
    streaming.delete(threadKey);
  }

  // A stream that STOPPED names a message that will never become a row. Keep
  // its placeholder, settled, as the only record of what was in flight, and
  // cancel the tools that never reported an outcome.
  const abortSettled: ToolExecutionStateUpdate[] = [];
  const settledThreadKeys = new Set<string>();
  if (abortedThreadKeys.size > 0 || batchAbortedIds.size > 0) {
    for (const [threadKey, msg] of [...streaming]) {
      if (!(batchAbortedIds.has(msg.id) || abortedThreadKeys.has(threadKey))) continue;
      const { message: settled, cancelledToolCallIds } = settleCancelledStreamBlocks(msg, toolCallsBeforeAbort);
      for (const toolCallId of cancelledToolCallIds) {
        abortSettled.push(inferredCancel(chatId, toolCallId, toolCallsBeforeAbort.get(toolCallId)?.toolName, now()));
      }
      const visible = settled.contentBlocks.some(
        (b) => b.type === ContentBlockType.TOOL_CALL || (b.content || "").length > 0,
      );
      if (visible) streaming.set(threadKey, settled);
      else streaming.delete(threadKey);
      settledThreadKeys.add(threadKey);
    }
  }
  // The marker can ride the SAME batch as the deltas it ends; those deltas were
  // dropped as a stale tail, but the tool status they published is still held.
  if (batchAbortedIds.size > 0) {
    const done = new Set(abortSettled.map((u) => u.tool_call_id));
    for (const delta of rawDeltas) {
      const toolCallId = delta.tool_call?.id;
      if (!toolCallId || !delta.message_id || !batchAbortedIds.has(delta.message_id) || done.has(toolCallId)) continue;
      const known = toolCallsBeforeAbort.get(toolCallId);
      if (toolStatusSurvivesStreamAbort(known?.status)) continue;
      done.add(toolCallId);
      abortSettled.push(inferredCancel(chatId, toolCallId, known?.toolName || delta.tool_call?.name, now()));
    }
  }

  // Retire any placeholder whose id was finalized this batch, except one the
  // abort pass just settled.
  for (const [threadKey, msg] of [...streaming]) {
    if (settledThreadKeys.has(threadKey)) continue;
    if (newlyFinalizedIds.has(msg.id)) streaming.delete(threadKey);
  }

  nextMessages = [...nextMessages].sort(compareMessagesWithinThread);

  const finalizedStreamIds =
    newlyFinalizedIds.size === 0 && !isSnapshot
      ? state.finalizedStreamIds
      : new Set([...(snapshotReplaces ? [] : state.finalizedStreamIds), ...newlyFinalizedIds]);

  return {
    ...state,
    messages: nextMessages,
    toolResults,
    toolCalls:
      abortSettled.length > 0
        ? applyToolCallStateUpdates(toolCallsBeforeAbort, abortSettled, chatId)
        : toolCallsBeforeAbort,
    streaming: Object.fromEntries(streaming),
    finalizedStreamIds,
    approvals: applyApprovals(prior.approvals, of<ToolApprovalUpdate>("approval")),
    threads: mergeActiveThreads(prior.threads, of<ActiveThreadUpdate>("thread")),
    ...applyQuestions(prior, of<QuestionUpdate>("question")),
    errors: applyLogSlice(state.errors, of<ErrorUpdate>("error"), isSnapshot, { key: (e) => e.id }),
    info: applyLogSlice(
      state.info,
      [...of<InfoUpdate>("info"), ...of<InfoUpdate>("warning")],
      isSnapshot,
      {
        key: (e) => e.id,
        merge: (prev, next) => ({ ...next, timestamp: prev.timestamp || next.timestamp }),
      },
    ),
    runOutputs: applyLogSlice(state.runOutputs, of<RunOutputUpdate>("run_output"), isSnapshot, {
      key: (r) => r.unique_activity_id || "",
      sort: bySequenceNumber,
    }),
    nodeExecutions: applyLogSlice(state.nodeExecutions, of<NodeExecutionUpdate>("node_execution"), isSnapshot, {
      // Per run: two runs of one workflow share every node id.
      key: (n) => `${n.workflow_id}:${n.node_id}:${n.event_type}`,
      sort: bySequenceNumber,
    }),
    workflowStatus: applyWorkflowStatus(prior.workflowStatus, of<WorkflowStatusUpdate>("workflow_status")),
    metadata: of<import("./events.js").ChatMetadataUpdate>("chat").at(-1) ?? state.metadata,
  };
}

function inferredCancel(
  chatId: string,
  toolCallId: string,
  toolName: string | undefined,
  timestamp: string,
): ToolExecutionStateUpdate {
  return {
    update_type: "tool_execution_state",
    id: toolCallId,
    chat_id: chatId,
    tool_call_id: toolCallId,
    tool_name: toolName || "",
    status: "cancelled",
    node_id: "",
    sequence_number: 0,
    timestamp,
    // Deduced from the stream ending, not reported by the server. A tool past
    // the point of no return still finishes, and that real outcome corrects it.
    inferred: true,
  };
}

// ── Streaming placeholder ───────────────────────────────────────────────────

/**
 * Fold one thread's streaming deltas onto its placeholder (null: nothing to
 * show, e.g. a tail of deltas of a stream that already ended).
 */
function foldStreamingDeltas(
  deltas: StreamingDelta[],
  current: Message | null,
  chatId: string,
  toolCalls: ReadonlyMap<string, ToolCallState>,
  timestamp: string,
): Message | null {
  // Blocks are addressed by their wire block_index, never by array position:
  // the placeholder holds them compacted (a thinking block takes an index but
  // no slot), so re-spread them at their own index before folding more deltas.
  let blocks: StreamBlock[] = [];
  for (const block of (current?.contentBlocks ?? []) as StreamBlock[]) blocks[block.index] = block;
  let streamCancelled = false;

  const thread = deltas.find((d) => d.thread)?.thread ?? current?.thread;
  // Delta identity: all deltas of a thread carry the same pre-allocated id, which
  // the placeholder adopts instead of a fabricated one.
  const deltaMessageId = deltas.find((d) => d.message_id)?.message_id;

  for (const delta of deltas) {
    switch (delta.delta_type) {
      case "message_start":
        // A retry re-streams the same message id from block 0: drop the stale attempt.
        blocks = [];
        break;
      case "thinking_block_start":
      case "thinking_block_delta":
        break;
      case "content_block_start":
        blocks[delta.block_index] = {
          id: "",
          type: ContentBlockType.TEXT,
          index: delta.block_index,
          content: "",
        } as StreamBlock;
        break;
      case "content_block_delta": {
        const block = blocks[delta.block_index];
        if (block) blocks[delta.block_index] = { ...block, content: (block.content || "") + (delta.delta || "") };
        break;
      }
      case "tool_use_start":
        if (delta.tool_call) {
          blocks[delta.block_index] = {
            id: "",
            type: ContentBlockType.TOOL_CALL,
            index: delta.block_index,
            toolCallId: delta.tool_call.id,
            toolName: delta.tool_call.name,
          } as StreamBlock;
        }
        break;
      case "tool_use_stop":
        // The full input comes with the persisted message.
        break;
      case "tool_cancelled":
        if (delta.tool_call) {
          const existing = blocks[delta.block_index];
          blocks[delta.block_index] =
            existing && existing.type === ContentBlockType.TOOL_CALL
              ? { ...existing, status: "cancelled" }
              : ({
                  id: "",
                  type: ContentBlockType.TOOL_CALL,
                  index: delta.block_index,
                  toolCallId: delta.tool_call.id,
                  toolName: delta.tool_call.name,
                  status: "cancelled",
                } as StreamBlock);
        }
        break;
      case "stream_cancelled":
        streamCancelled = true;
        // Only tools that never reached an outcome. `block.status` alone cannot
        // say: nothing writes "completed" onto a streaming block (success
        // arrives on the tool-state channel).
        blocks = blocks.map((block) => {
          if (!block || block.type !== ContentBlockType.TOOL_CALL || block.status) return block;
          const status = block.toolCallId ? toolCalls.get(block.toolCallId)?.status : undefined;
          return status === "completed" || status === "failed" ? block : { ...block, status: "cancelled" };
        });
        break;
    }
  }

  const compact = blocks.filter((b) => b !== undefined && b !== null);
  // A tail of deltas from an already-finalized stream must not fabricate an
  // empty placeholder.
  if (!current && compact.length === 0) return null;

  const normalizedThread = normalizeThreadKey(chatId, thread);
  return {
    id: deltaMessageId || current?.id || `streaming-temp-${normalizedThread}`,
    chatId: current?.chatId || "",
    role: MessageRole.ASSISTANT,
    contentBlocks: compact,
    createdAt: current?.createdAt || timestamp,
    updatedAt: timestamp,
    streamingState: streamCancelled ? StreamingState.COMPLETE : StreamingState.STREAMING,
    seq: current?.seq ?? PLACEHOLDER_SEQ,
    thread: thread || "",
    sequenceNumber: 0n,
    attachments: current?.attachments || [],
  } as Message;
}

// ── Small slices ────────────────────────────────────────────────────────────

const defined = <T extends object>(value: T): Partial<T> =>
  Object.fromEntries(Object.entries(value).filter(([, v]) => v !== undefined)) as Partial<T>;

/** Insert an approval, or merge the fields the event carries over the one held (a resolution does not carry the title). */
function applyApprovals(existing: ToolApprovalUpdate[], updates: ToolApprovalUpdate[]): ToolApprovalUpdate[] {
  if (updates.length === 0) return existing;
  const next = [...existing];
  for (const update of updates) {
    const i = next.findIndex((a) => a.id === update.id);
    if (i >= 0) next[i] = { ...next[i]!, ...defined(update) };
    else next.push(update);
  }
  return next;
}

function applyQuestions(
  state: Pick<ChatState, "pendingQuestions" | "asyncQuestions">,
  updates: QuestionUpdate[],
): Pick<ChatState, "pendingQuestions" | "asyncQuestions"> {
  let pending = state.pendingQuestions;
  let async = state.asyncQuestions;
  for (const q of updates) {
    if (q.async) {
      async = async.filter((x) => x.question_id !== q.question_id);
      async = [...async, q];
    } else if (q.status === "pending") {
      pending = [...pending.filter((x) => x.question_id !== q.question_id), q];
    } else if (q.status === "resolved") {
      pending = pending.filter((x) => x.question_id !== q.question_id);
    }
  }
  return { pendingQuestions: pending, asyncQuestions: async };
}

function applyWorkflowStatus(
  existing: Readonly<Record<string, WorkflowStatusUpdate>>,
  updates: WorkflowStatusUpdate[],
): Readonly<Record<string, WorkflowStatusUpdate>> {
  if (updates.length === 0) return existing;
  const next = { ...existing };
  for (const u of updates) next[u.workflow_id] = u;
  return next;
}
