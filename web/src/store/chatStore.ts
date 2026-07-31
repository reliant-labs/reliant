import { create } from "zustand";
import {
  api,
  type Chat,
  type Message,
  type ToolApprovalRequest,
} from "../api/client";
import type { ContentBlock } from "../types/chat";
import {
  MessageRole,
  ContentBlockType,
  StreamingState,
  ChatState,
} from "../gen/reliant/v1/chat_pb";
import { ApprovalStatus } from "../gen/reliant/v1/approval_pb";
import type { ToolResultsByCallId } from "../lib/messageProcessor";
import { getProcessedMessage } from "../lib/messageProcessor";
import { sortMessagesForDisplay } from "../lib/messageOrder";
import {
  CHAT_MARKER_KINDS,
  extractChatMarker,
  stripChatMarker,
} from "../lib/chatMarkers";
import {
  applyLogSlice,
  applyToolCallStateUpdates,
  bySequenceNumber,
  mergeActiveThreads,
  mergeMessages,
  resolveStreamingBase,
} from "../lib/chatStreamReducers";

import * as Sentry from "@sentry/react";

// Chat streaming is handled via the unified gRPC stream in globalUpdatesStore
import type {
  ChatUpdate,
  ProtoMessageUpdate,
  ToolApprovalUpdate,
  ActiveThreadUpdate,
  ToolCallUpdate,
  ErrorUpdate,
  InfoUpdate,
  WorkflowStatusUpdate,
  StreamingDelta,
  StreamFinalizedUpdate,
  RunOutputUpdate,
  NodeExecutionUpdate,
  QuestionUpdate,
} from "../types/streaming";
import { questionGrpc } from "../api/question-grpc";

// ToolExecutionStateUpdate is a CLIENT-SIDE type synthesized from ToolCallUpdate.
// The backend sends tool_call updates, which the frontend converts to this format
// for internal state tracking. It is NOT sent over the wire.
export interface ToolExecutionStateUpdate {
  update_type: "tool_execution_state";
  id: string;
  chat_id: string;
  tool_call_id: string;
  tool_name: string;
  status:
    | "pending"
    | "executing"
    | "completed"
    | "failed"
    | "denied"
    | "cancelled";
  node_id: string;
  sequence_number: number;
  timestamp: string;
}
import { triggerRefetch, type RefetchType } from "../store/refetchStore";

import { useProjectStore } from "./projectStore";
import { useActivityStore, ChatActivity } from "./activityStore";
import { useThreadActivityStore } from "./threadActivityStore";
import { useChatNavigationStore } from "./chatNavigationStore";
import { useTasksStore } from "./tasksStore";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useWorktreeStore } from "./worktreeStore";
import { useChatParamsStore } from "./chatParamsStore";
import { useAttachmentStore } from "./attachmentStore";
import { useModalStore } from "./modalStore";
import type { Attachment } from "../api/client";
import { logger } from "../lib/logger";
import { singleflight } from "../lib/singleflight";
import { queryClient } from "../lib/query-client";
import {
  approvalKeys,
  upsertApprovalInCache,
  patchPendingQuestionCache,
} from "../hooks/approval-queries";
import {
  chatKeys,
  seedChatDetail,
  patchChatCaches,
  getChatFromCache,
} from "../hooks/chat-queries";
import {
  setMessagesInCache,
  patchMessagesCache,
  getMessagesFromCache,
  hasMessagesCache,
  clearMessagesCache,
  clearAllMessagesCache,
} from "../hooks/message-queries";
import { DEFAULT_WORKFLOW } from "./preferencesStore";
import { tabSwitchProfiler } from "../lib/tabSwitchProfiler";
import {
  STREAMING_FLUSH_TIMEOUT_MS,
} from "../lib/constants";
import { trackEvent } from "../lib/analytics";

// Lazy getter to avoid circular dependency with globalUpdatesStore.
// `var` (not `let`) is intentional: globalUpdatesStore.ts calls
// initGlobalUpdatesStoreRef() from its module body, which can fire BEFORE
// this line executes when the import graph cycles through chatStore. `var`
// is hoisted (initialized to undefined at function/module scope), so the
// assignment in initGlobalUpdatesStoreRef never hits a temporal dead zone.
// eslint-disable-next-line no-var
var _globalUpdatesStoreModule: typeof import("./globalUpdatesStore") | null = null;

function getGlobalUpdatesStore() {
  if (!_globalUpdatesStoreModule) {
    logger.warn("getGlobalUpdatesStore called before module initialized — skipping");
    return null;
  }
  return (_globalUpdatesStoreModule as Awaited<typeof _globalUpdatesStoreModule>).useGlobalUpdatesStore.getState();
}

/** Called once from globalUpdatesStore after it finishes initializing. */
export function initGlobalUpdatesStoreRef(mod: typeof import("./globalUpdatesStore")) {
  _globalUpdatesStoreModule = mod;
}

function toApprovalStatus(
  status: "pending" | "approved" | "denied",
): ApprovalStatus {
  switch (status) {
    case "approved":
      return ApprovalStatus.APPROVED;
    case "denied":
      return ApprovalStatus.DENIED;
    default:
      return ApprovalStatus.PENDING;
  }
}

// ============================================================================
// Streaming Delta Buffer - batch updates on newlines for fewer re-renders
// ============================================================================

interface StreamingBuffer {
  deltas: StreamingDelta[];
  pendingText: string; // Accumulated text that doesn't end in newline yet
  flushTimeoutId: ReturnType<typeof setTimeout> | null;
}

// Buffers are keyed by "chatId:thread" to support multiple simultaneous streams
const streamingBuffers = new Map<string, StreamingBuffer>();

// Chat-error marker routing.
//
// Some workflow / driver error classes are surfaced through the chat-error
// stream (ErrorUpdate.error_message) with a stable bracketed tail that
// survives Temporal's JSON stringification. The wire contract — kind strings,
// regex, wrap format — lives in `../lib/chatMarkers.ts` and is mirrored in
// `reliant/internal/chatmarkers/markers.go`. This file just routes on the
// kind and converts the structured signal into UI side effects.
//
// IMPORTANT: the quota path only fires for the reliant-managed driver.
// User-provided keys to OpenAI / Anthropic / etc. surface their own
// provider's 429s unchanged, with no marker, and the upgrade modal does NOT
// open for them. That's intentional: the user owns those billing
// relationships, not us.

const DEFAULT_RELIANT_UPGRADE_URL = "/billing/plans";

// Single-fire guard: a burst of error events (one per workflow retry attempt
// before the workflow gives up) opens exactly one modal, not N. Mirrors the
// upgradeInterceptor pattern in api/grpc-client.ts. Reset when the modal
// closes so a later quota hit in a different chat still surfaces a modal.
let _reliantManagedQuotaModalInFlight = false;

function openReliantManagedQuotaModal(
  errorMessage: string,
  upgradeUrl: string,
): void {
  if (_reliantManagedQuotaModalInFlight) return;
  // Don't reopen if the modal is already up (e.g. a fresh chat hits the same
  // wall while the previous chat's modal is still showing).
  if (useModalStore.getState().activeModal === "upgrade-required") {
    _reliantManagedQuotaModalInFlight = true;
    const unsub = useModalStore.subscribe((s) => {
      if (s.activeModal !== "upgrade-required") {
        _reliantManagedQuotaModalInFlight = false;
        unsub();
      }
    });
    return;
  }

  _reliantManagedQuotaModalInFlight = true;
  // Strip the marker tail from the message so the user-facing copy is clean.
  const cleanMessage = stripChatMarker(errorMessage);
  useModalStore.getState().openModal("upgrade-required", {
    // Mirrors the canonical code used by the Connect-side enforcement path
    // (control-plane/internal/enforcement/check.go) so UpgradeRequiredModal
    // shows the same "Free tier quota exceeded" copy regardless of which
    // path tripped it.
    reason: "free_tier_global_budget",
    message: cleanMessage,
    upgradeUrl,
  });
  const unsub = useModalStore.subscribe((s) => {
    if (s.activeModal !== "upgrade-required") {
      _reliantManagedQuotaModalInFlight = false;
      unsub();
    }
  });
}

/**
 * Inspect an incoming error update, route on the embedded chat marker (if
 * any), and either trigger side effects (e.g. open the upgrade modal) or
 * mutate `errorUpdate.error_message` to strip the marker tail before it
 * reaches the chat-error UI.
 *
 * `isSnapshot` suppresses live-only side effects (modal pop) on replayed
 * snapshot events — the cleanup mutation is idempotent and runs in both
 * cases so the user sees the same clean message either way.
 *
 * TODO(daemon-offline-halt): upgrade the DaemonOfflineHalt branch to a
 * non-modal banner with a "Reconnect workspace" action that links to the
 * daemon detail page. For now we just strip the marker and let the cleaned
 * message flow through the regular chat-error UI.
 */
function routeChatErrorMarker(
  errorUpdate: { error_message: string },
  isSnapshot: boolean,
): void {
  const marker = extractChatMarker(errorUpdate.error_message);
  if (marker === null) return;

  switch (marker.kind) {
    case CHAT_MARKER_KINDS.ReliantManagedQuotaExhausted: {
      // Modal is a LIVE side-effect — skip on snapshot replays so switching
      // back into the chat doesn't re-pop the modal.
      if (!isSnapshot) {
        const upgradeUrl =
          marker.payload.trim() || DEFAULT_RELIANT_UPGRADE_URL;
        openReliantManagedQuotaModal(errorUpdate.error_message, upgradeUrl);
      }
      // Note: the quota modal helper strips the marker for its OWN displayed
      // copy via stripChatMarker, but the underlying chat-error event still
      // carries the marker until we strip it here. Strip on both live and
      // snapshot paths so the timeline render is clean either way.
      errorUpdate.error_message = stripChatMarker(errorUpdate.error_message);
      return;
    }
    case CHAT_MARKER_KINDS.DaemonOfflineHalt: {
      // Strip the marker tail from the message so the user-facing copy is
      // clean. The stripped message still includes the human-readable
      // "daemon offline for N consecutive turns" prefix, which is fine to
      // show as a normal workflow error for now. Idempotent on both
      // snapshot and live paths.
      errorUpdate.error_message = stripChatMarker(errorUpdate.error_message);
      return;
    }
    default: {
      // Exhaustiveness guard: a new kind landed in chatMarkers.ts without
      // being routed here. TypeScript will flag this at compile time.
      const _exhaustive: never = marker.kind;
      void _exhaustive;
    }
  }
}

// Normalize thread to a consistent key - main thread uses chatId
function normalizeThreadKey(
  chatId: string,
  thread: string | undefined,
): string {
  // Main thread: undefined, "0", or chatId itself
  if (!thread || thread === "0" || thread === chatId) {
    return chatId;
  }
  return thread;
}

// Create composite key for buffer lookup
function getBufferKey(chatId: string, thread: string | undefined): string {
  const threadKey = normalizeThreadKey(chatId, thread);
  return `${chatId}:${threadKey}`;
}

// Get or create buffer for a chat+thread
function getStreamingBuffer(
  chatId: string,
  thread: string | undefined,
): StreamingBuffer {
  const key = getBufferKey(chatId, thread);
  let buffer = streamingBuffers.get(key);
  if (!buffer) {
    buffer = {
      deltas: [],
      pendingText: "",
      flushTimeoutId: null,
    };
    streamingBuffers.set(key, buffer);
  }
  return buffer;
}

// Check if a buffer exists for a chat+thread
function hasStreamingBuffer(
  chatId: string,
  thread: string | undefined,
): boolean {
  const key = getBufferKey(chatId, thread);
  return streamingBuffers.has(key);
}

// Clear buffer for a specific chat+thread
function clearStreamingBuffer(
  chatId: string,
  thread?: string | undefined,
): void {
  const key = getBufferKey(chatId, thread);
  const buffer = streamingBuffers.get(key);
  if (buffer?.flushTimeoutId) {
    clearTimeout(buffer.flushTimeoutId);
  }
  streamingBuffers.delete(key);
}

// Clear ALL streaming buffers for a chat (every thread). Used when the
// chat's activity goes idle and any leftover buffer is stale by definition.
function clearStreamingBuffersForChat(chatId: string): void {
  for (const key of [...streamingBuffers.keys()]) {
    if (key.startsWith(`${chatId}:`)) {
      const buffer = streamingBuffers.get(key);
      if (buffer?.flushTimeoutId) {
        clearTimeout(buffer.flushTimeoutId);
      }
      streamingBuffers.delete(key);
    }
  }
}

// ============================================================================
// Workflow event-log reducers
//
// errorEvents / infoEvents / runOutputs / nodeExecutions are stream-only
// ephemeral logs: appended live, replaced wholesale on a snapshot (isSnapshot),
// and deduped by a per-type key. The pure reducers live in
// lib/chatStreamReducers.ts; behavior is pinned by
// chatStore.streamEvents.test.ts.
// ============================================================================

/** Errors dedup by id (replace in place); snapshot replaces the whole list. */
const applyErrorUpdates = (
  existing: ErrorUpdate[],
  updates: ErrorUpdate[],
  isSnapshot: boolean,
): ErrorUpdate[] =>
  applyLogSlice(existing, updates, isSnapshot, { key: (e) => e.id });

/**
 * Info notifications dedup by id, but a re-delivery keeps the ORIGINAL
 * timestamp so its place in the timeline stays stable.
 */
const applyInfoUpdates = (
  existing: InfoUpdate[],
  updates: InfoUpdate[],
  isSnapshot: boolean,
): InfoUpdate[] =>
  applyLogSlice(existing, updates, isSnapshot, {
    key: (e) => e.id,
    merge: (prev, next) => ({ ...next, timestamp: prev.timestamp || next.timestamp }),
  });

/** Run outputs dedup by unique_activity_id, then sort by sequence_number. */
const applyRunOutputUpdates = (
  existing: RunOutputUpdate[],
  updates: RunOutputUpdate[],
  isSnapshot: boolean,
): RunOutputUpdate[] =>
  applyLogSlice(existing, updates, isSnapshot, {
    key: (r) => r.unique_activity_id || "",
    sort: bySequenceNumber,
  });

/** Node executions dedup by node_id + event_type, then sort by sequence_number. */
const applyNodeExecutionUpdates = (
  existing: NodeExecutionUpdate[],
  updates: NodeExecutionUpdate[],
  isSnapshot: boolean,
): NodeExecutionUpdate[] =>
  applyLogSlice(existing, updates, isSnapshot, {
    key: (n) => `${n.node_id}:${n.event_type}`,
    sort: bySequenceNumber,
  });

// Buffer streaming deltas and flush on newlines
// Returns deltas that should be processed immediately
// Thread-aware: groups deltas by thread and buffers each separately
function bufferStreamingDeltas(
  chatId: string,
  deltas: StreamingDelta[],
  flushCallback: (deltas: StreamingDelta[], thread: string | undefined) => void,
): StreamingDelta[] {
  const immediateDeltas: StreamingDelta[] = [];

  // Group deltas by thread first
  const deltasByThread = new Map<string | undefined, StreamingDelta[]>();
  for (const delta of deltas) {
    const thread = delta.thread;
    if (!deltasByThread.has(thread)) {
      deltasByThread.set(thread, []);
    }
    deltasByThread.get(thread)!.push(delta);
  }

  // Process each thread's deltas separately
  for (const [thread, threadDeltas] of deltasByThread) {
    const buffer = getStreamingBuffer(chatId, thread);
    const bufferKey = getBufferKey(chatId, thread);

    for (const delta of threadDeltas) {
      // Skip thinking deltas entirely - not displayed, causes memory issues
      if (
        delta.delta_type === "thinking_block_start" ||
        delta.delta_type === "thinking_block_delta"
      ) {
        continue;
      }

      // Non-content deltas (tool_use_start, etc.) flush immediately
      if (delta.delta_type !== "content_block_delta") {
        // Flush any pending content first
        if (buffer.deltas.length > 0) {
          immediateDeltas.push(...buffer.deltas);
          buffer.deltas = [];
          buffer.pendingText = "";
        }
        immediateDeltas.push(delta);
        continue;
      }

      // Content delta - check for newlines
      const text = delta.delta || "";
      buffer.pendingText += text;
      buffer.deltas.push(delta);

      // If we have a newline, flush the buffer
      if (text.includes("\n")) {
        immediateDeltas.push(...buffer.deltas);
        buffer.deltas = [];
        buffer.pendingText = "";

        // Clear any pending flush timeout
        if (buffer.flushTimeoutId) {
          clearTimeout(buffer.flushTimeoutId);
          buffer.flushTimeoutId = null;
        }
      } else {
        // No newline - set a timeout to flush if we don't get one soon
        if (buffer.flushTimeoutId) {
          clearTimeout(buffer.flushTimeoutId);
        }
        buffer.flushTimeoutId = setTimeout(() => {
          const buf = streamingBuffers.get(bufferKey);
          if (buf && buf.deltas.length > 0) {
            flushCallback(buf.deltas, thread);
            buf.deltas = [];
            buf.pendingText = "";
          }
        }, STREAMING_FLUSH_TIMEOUT_MS);
      }
    }
  }

  return immediateDeltas;
}


type AttachmentMetadataInput = {
  id?: unknown;
  filename?: unknown;
  size?: unknown;
  mimeType?: unknown;
  mime_type?: unknown;
  url?: unknown;
};

function normalizeAttachmentSize(size: unknown): bigint {
  if (typeof size === "bigint") {
    return size;
  }
  if (typeof size === "number" && Number.isFinite(size)) {
    return BigInt(Math.max(0, Math.trunc(size)));
  }
  if (typeof size === "string" && size.trim() !== "") {
    try {
      return BigInt(size);
    } catch {
      return BigInt(0);
    }
  }
  return BigInt(0);
}

function normalizeMessageAttachments(rawAttachments: unknown): Attachment[] {
  if (!Array.isArray(rawAttachments)) {
    return [];
  }

  return rawAttachments
    .map((attachment): Attachment | null => {
      if (!attachment || typeof attachment !== "object") {
        return null;
      }

      const raw = attachment as AttachmentMetadataInput;
      const id = typeof raw.id === "string" ? raw.id : "";
      if (!id) {
        return null;
      }

      const filename =
        typeof raw.filename === "string" && raw.filename !== ""
          ? raw.filename
          : "file";
      const mimeType =
        typeof raw.mimeType === "string"
          ? raw.mimeType
          : typeof raw.mime_type === "string"
            ? raw.mime_type
            : "";
      const url =
        typeof raw.url === "string" && raw.url !== ""
          ? raw.url
          : `/api/attachments/${id}`;

      return {
        id,
        filename,
        size: normalizeAttachmentSize(raw.size),
        mimeType,
        url,
      } as Attachment;
    })
    .filter((attachment): attachment is Attachment => attachment !== null);
}

function normalizeMessageAttachmentFields(message: Message): Message {
  return {
    ...message,
    attachments: normalizeMessageAttachments(message.attachments),
  };
}

// Helper to look up attachment metadata from the attachment store by IDs
// Returns proto Attachment[] for use in optimistic messages
function getAttachmentsFromStore(attachmentIds: string[]): Attachment[] {
  if (!attachmentIds || attachmentIds.length === 0) {
    return [];
  }

  const attachmentStore = useAttachmentStore.getState();
  const allAttachments: Attachment[] = [];

  // Search through all sessions in the attachment store
  attachmentStore.attachments.forEach((attachments) => {
    for (const att of attachments) {
      if (attachmentIds.includes(att.id)) {
        const [normalizedAttachment] = normalizeMessageAttachments([att]);
        if (normalizedAttachment) {
          allAttachments.push(normalizedAttachment);
        }
      }
    }
  });

  return allAttachments;
}

// Track which task-related tool call IDs have already been processed
// to prevent duplicate API calls when messages are reprocessed (cross-matching, streaming updates)
const processedTaskToolCallIds = new Set<string>();



// Helper to check if content blocks indicate a streaming (incomplete) message
function isMessageBlocksStreaming(contentBlocks: ContentBlock[]): boolean {
  if (!contentBlocks || contentBlocks.length === 0) {
    return false;
  }

  return contentBlocks.some((block) => {
    if (block.type === ContentBlockType.TEXT) {
      return block.content === undefined || block.content === "";
    }
    if (block.type === ContentBlockType.TOOL_CALL) {
      return block.input === undefined;
    }
    if (block.type === ContentBlockType.TOOL_RESULT) {
      return block.content === undefined;
    }
    return false;
  });
}

// Helper to check if a proto Message is complete
function isProtoMessageComplete(msg: Message): boolean {
  const blocks = msg.contentBlocks;
  if (!blocks || blocks.length === 0) return false;
  return !isMessageBlocksStreaming(blocks as ContentBlock[]);
}

/**
 * Centralized chat activity state.
 * This is the SINGLE SOURCE OF TRUTH for all activity indicators:
 * - Sidebar green dot
 * - Chat thinking indicator
 * - Thread tab activity indicators
 */
// NOTE: Activity state is managed by activityStore.ts (SINGLE SOURCE OF TRUTH)

// Individual chat state
// Tool call state tracking
export interface ToolCallState {
  id: string;
  sessionId: string;
  toolName: string;
  status:
    | "pending" // Queued but not started
    | "preparing" // LLM is writing the tool request (streaming input)
    | "requested" // LLM finished writing, ready for approval/execution
    | "writing_input" // Legacy: use "preparing" instead
    | "executing" // Currently executing
    | "cancelling" // Cancellation requested
    | "cancelled" // Cancelled by user
    | "completed" // Successfully completed
    | "denied" // Denied approval
    | "failed"; // Execution failed
  timestamp: string;
  error?: string;
  // Unified approval state
  approval?: ToolApprovalRequest;
  needsApproval?: boolean;
  // Tool call data from messages (for UI rendering)
  toolCall?: {
    name: string;
    input: Record<string, unknown> | string;
    finished?: boolean;
  };
  // Tool result data
  result?: {
    content?: string;
    is_error?: boolean;
    [key: string]: unknown;
  };
}

// Dismiss response from API
// NORMALIZED STORE STRUCTURE
// Each piece of chat state is stored separately for optimal Zustand subscriptions
interface ChatStoreState {
  // Chat objects are homed in the React Query caches (chatKeys.detail /
  // chatKeys.list) — the single source of truth. Read them via
  // chat-queries (useChat / getChatFromCache / getCachedChatList).
  //
  // Per-chat MESSAGES are likewise homed in the React Query cache
  // (messageKeys.list) — the single source of truth. Read via useChatMessages /
  // getMessagesFromCache; write via the message-queries helpers
  // (setMessagesInCache / patchMessagesCache). There is no messages field here.
  discussMode: Record<string, boolean>;
  errorEvents: Record<string, ErrorUpdate[]>; // Error events from workflow/activity failures
  infoEvents: Record<string, InfoUpdate[]>; // Info notifications (shown to user, not saved to thread)
  runOutputs: Record<string, RunOutputUpdate[]>; // Run step outputs from workflow execution
  nodeExecutions: Record<string, NodeExecutionUpdate[]>; // Node execution events from workflow activities
  // NOTE: chatActivity has been REMOVED - use activityStore instead
  // Activity state comes from activityStore, populated by the server's ChatActivity enum
  toolCallStates: Record<string, Map<string, ToolCallState>>;
  streamingMessages: Record<string, Record<string, Message | null> | undefined>; // Currently streaming messages per chat+thread (temporary, replaced by complete message)

  // Delta identity protocol: per-chat set of assistant message ids whose stream
  // has finalized. Seeded from (a) persisted stream_finalized markers (live +
  // snapshot replay) and (b) every complete persisted ASSISTANT message id.
  // Once an id is here, any streaming delta stamped with it is a stale tail and
  // is dropped BEFORE a placeholder is built — that is what makes the
  // phantom-at-end-of-chat impossible. Snapshot replaces the set (snapshot
  // semantics); cleared in evictChat/reset. Deltas without a message_id (old
  // servers) bypass this entirely and take the legacy thread-keyed path.
  finalizedStreamIds: Record<string, Set<string>>;

  // Context usage tracking for compaction indicator (per chat, per thread)
  contextUsage: Record<
    string, // chatId
    Record<
      string, // threadId
      {
        threadTokenCount: number;
        compactionThreshold: number;
      }
    >
  >;


  // Normalized tool results, indexed by chatId -> tool_call_id. Tool results
  // arrive as separate TOOL-role messages; this index is the single place they
  // live, and processMessage resolves each tool call's result from it at read
  // time (the call→result join), instead of embedding results into assistant
  // message blocks.
  toolResultsByCallId: Record<string, ToolResultsByCallId>;

  // Track which chat is currently active (for UI purposes)
  activeChatId: string | null;

  // Global loading/error states
  hasLoaded: boolean;
  error: string | null;

  // Track pending status fetches to prevent duplicate API calls
  pendingStatusFetches: Record<string, Promise<unknown>>;

  // Methods for chat management
  loadChats: (projectId?: string) => Promise<void>;
  createChat: (
    worktreeId?: string,
    firstMessage?: string,
    attachmentIds?: string[],
    workflowParams?: Record<string, unknown>,
    workflow?: string | null,
    selectedPresets?: Record<string, string>,
  ) => Promise<Chat>;
  // Methods for chat state management
  initChatState: (chat: Chat) => void;

  // Methods for active chat
  setActiveChat: (chatId: string | null) => void;
  selectChat: (chat: Chat) => void;
  clearCurrentChat: (worktreeId?: string | null) => void;

  // Message methods
  sendMessage: (
    chatId: string,
    content: string,
    attachmentIds?: string[],
    options?: {
      workflow?: string | null;
      workflowParams?: Record<string, unknown>;
      targetThread?: string | null;
      selectedPresets?: Record<string, string>;
      systemMessages?: Array<{ role: "system"; content: string }>; // System messages to prepend
      discuss?: boolean; // If true, chat with LLM without resuming paused workflow
    },
  ) => Promise<void>;
  loadMessages: (chatId: string) => Promise<void>;

  // Stream methods (chat detail events are delivered via the unified global stream)
  processChatStreamUpdates: (chatId: string, updates: ChatUpdate[], isSnapshot?: boolean) => void;
  // Clears streaming placeholders/buffers for a chat. Called when the chat's
  // activity goes IDLE — the authoritative "nothing is streaming" signal —
  // so stale delta tails can't leave a phantom message at the end of the chat.
  clearStreamingState: (chatId: string) => void;

  // Chat control methods
  cancelChat: (chatId: string) => Promise<void>;
  pauseChat: (chatId: string) => Promise<void>;
  setDiscussMode: (chatId: string, enabled: boolean) => void;
  resumeChat: (chatId: string) => Promise<void>;

  resolveQuestion: (chatId: string, questionId: string, action: string, responseData?: string) => Promise<void>;
  refreshChat: (chatId: string) => Promise<void>;
  forceResetChatToIdle: (chatId: string) => void;
  checkChatStatus: (chatId: string) => Promise<void>;
  dismissChat: (chatId: string) => Promise<void>;

  // Tool approvals + pending questions are server data owned by the React
  // Query cache (see hooks/approval-queries.ts): approve/deny via the mutation
  // hooks, read via useApprovals / usePendingApprovals / usePendingQuestion.
  // The chat stream patches that cache directly — no store-side copy here.

  // Branch chat
  _navigateToBranchedChat: (newChat: Chat, worktreeId?: string) => void;
  branchChat: (chatId: string, messageId: string) => Promise<void>;
  branchChatToWorktree: (
    chatId: string,
    messageId: string,
    worktreeId: string,
    workspaceContext?: {
      sourceWorktreeId?: string;
      filesCopied?: string[];
      copyFilesEnabled?: boolean;
    },
  ) => Promise<void>;

  // Connection management
  onConnectionRestored: () => void;
  clearError: () => void;
  retryConnection: () => Promise<void>;

  // Tool call management methods
  updateToolCallState: (
    chatId: string,
    toolCallId: string,
    toolCall: ToolCallState,
  ) => void;
  updateToolCallStatePartial: (
    chatId: string,
    toolCallId: string,
    updates: Partial<ToolCallState>,
  ) => void;
  getToolCallState: (
    chatId: string,
    toolCallId: string,
  ) => ToolCallState | undefined;
  getUnifiedToolCallData: (
    chatId: string,
    toolCallId: string,
  ) =>
    | {
        toolCall?: {
          name: string;
          input: Record<string, unknown> | string;
          finished?: boolean;
        };
        result?: {
          content?: string;
          is_error?: boolean;
          [key: string]: unknown;
        };
        status?:
          | "pending"
          | "preparing"
          | "requested"
          | "writing_input"
          | "executing"
          | "cancelling"
          | "cancelled"
          | "backgrounded"
          | "completed"
          | "denied"
          | "failed";
        approval?: ToolApprovalRequest;
        needsApproval?: boolean;
      }
    | undefined;
  cancelToolCall: (chatId: string, toolCallId: string) => Promise<void>;
  convertToBackground: (chatId: string, toolCallId: string) => Promise<string>;
  getExecutingToolCalls: (chatId: string) => ToolCallState[];
  getIsChatBusy: (chatId: string) => boolean; // Computed busy state
  // Release ALL retained state for one chat (messages RQ cache + per-chat
  // Zustand slices + streaming buffers). Called when a chat is deleted or
  // archived — the caches use gcTime: Infinity, so without this a dead
  // chat's messages are held until logout.
  evictChat: (chatId: string) => void;
  reset: () => void;
}

/**
 * ⚠️ WARNING: Direct store access - use with caution! ⚠️
 *
 * This is the raw Zustand store. Direct access is discouraged in components.
 *
 * ✅ INSTEAD, use the safe selector hooks from './chatStoreHooks':
 *    - useActiveChatId() - Get active chat ID
 *    - useActiveChat() - Get active chat object
 *    - useChatMessages(chatId) - Get messages for a chat
 *    - useSelectChat() - Get the selectChat action
 *    ... and many more
 *
 * WHY?
 * - Type safety: Hooks guarantee correct field access
 * - Performance: Pre-configured selectors with proper equality checks
 * - Maintainability: Single place to change if store structure evolves
 * - Prevents footguns: Can't accidentally duplicate state across stores
 *
 * Direct store access is ONLY allowed in:
 * - Other Zustand stores (e.g., chatNavigationStore calling chatStore methods)
 * - Store implementation files
 * - Integration/migration code (mark with TODO to refactor)
 *
 * If you're in a component and using useChatStore() directly, refactor to use
 * the hooks from './chatStoreHooks' instead!
 */
export const useChatStore = create<ChatStoreState>((set, get) => ({
  // Initial state
  discussMode: {},
  errorEvents: {},
  infoEvents: {},
  runOutputs: {},
  nodeExecutions: {},
  toolCallStates: {},
  streamingMessages: {}, // Currently streaming messages per chat+thread
  finalizedStreamIds: {}, // Delta identity: finalized assistant message ids per chat
  contextUsage: {}, // Context usage tracking for compaction indicator
  toolResultsByCallId: {}, // Normalized tool results per chat, keyed by tool_call_id
  activeChatId: null,
  hasLoaded: false,
  error: null,
  pendingStatusFetches: {},

  // Load all chats (with singleflight deduplication to prevent parallel API calls)
  loadChats: async () => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) {
      return;
    }

    // Use singleflight to ensure only one load per project runs at a time
    // The key includes projectId to allow different projects to load simultaneously
    return singleflight(`loadChats:${projectId}`, async () => {
      set({ error: null });
      try {
        const response = await api.chatsV2.list(projectId);
        const chatList = response.chats;
        const lastUserUpdateSequence = response.lastUserUpdateSequence;

        // Index the API chats by id for the activity merge below.
        const apiChatMap = new Map<string, Chat>();
        (chatList || []).forEach((chat: Chat) => {
          apiChatMap.set(chat.id, chat);
        });

        set({ hasLoaded: true });

        // Home each loaded chat into the React Query detail cache (the single
        // source of truth read by useChat / useActiveChat). The sidebar list
        // (chatKeys.list) is owned by useChatList's own fetch — we deliberately
        // do not write it here, to preserve its ordering. Imperative readers
        // enumerate that same list cache via getCachedChatList.
        for (const chat of apiChatMap.values()) {
          seedChatDetail(chat);
        }

        // Merge activity from ListChats into activityStore. Per-entry
        // precedence is decided by sequence ordering inside the store: the
        // envelope's lastUserUpdateSequence is the snapshot's freshness
        // watermark, so a list value only overwrites an entry set by a
        // stream event / optimistic write with a newer-or-equal seq claim.
        const serverActivities = new Map<string, ChatActivity>();
        for (const chat of apiChatMap.values()) {
          serverActivities.set(chat.id, (chat.activity ?? 0) as ChatActivity);
        }
        useActivityStore
          .getState()
          .applyListActivities(serverActivities, lastUserUpdateSequence);
        // Remove entries for chats no longer returned by the server
        // (deleted or moved to another project). An optimistically-created
        // chat may not be in the server list yet but is already homed in the
        // detail cache — keep its activity in that case.
        for (const chatId of [...useActivityStore.getState().activities.keys()]) {
          if (!apiChatMap.has(chatId) && !getChatFromCache(chatId)) {
            useActivityStore.getState().removeActivity(chatId);
          }
        }

        // Store the user update sequence for stream sync.
        // This must happen BEFORE globalUpdatesStore.connect() so the
        // stream starts from the right point.
        if (lastUserUpdateSequence > 0) {
          const globalStore = getGlobalUpdatesStore();
          if (globalStore) {
            const current = globalStore.lastSequence;
            if (lastUserUpdateSequence > current) {
              globalStore.setLastSequence(lastUserUpdateSequence);
            }
          }
        }
      } catch (error) {
        logger.error("Failed to load chats:", error);
        set({ error: "Failed to load chats", hasLoaded: true });
      }
    });
  },

  // Create a new chat
  createChat: async (
    worktreeId?: string,
    firstMessage: string = "Hello",
    attachmentIds?: string[],
    workflowParams?: Record<string, unknown>,
    workflow?: string | null,
    selectedPresets?: Record<string, string>,
  ) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) {
      throw new Error("No project selected");
    }

    const effectiveWorkflowParams: Record<string, unknown> = workflowParams ?? {};

    // For new chats, ALWAYS send the explicit workflow string.
    // Backend can resolve user defaults, but we've had cases where "" ended up as Agent.
    const effectiveWorkflow = workflow ?? DEFAULT_WORKFLOW;

    const chat = await api.chatsV2.create({
      project_id: projectId,
      messages: firstMessage
        ? [{ role: MessageRole.USER, content: firstMessage }]
        : [],
      attachments: attachmentIds,
      worktree_id: worktreeId,
      workflow: effectiveWorkflow,
      workflow_params:
        Object.keys(effectiveWorkflowParams).length > 0
          ? effectiveWorkflowParams
          : undefined,
      selectedPresets: selectedPresets,
    });

    const chatId = chat.id;

    // Home the new chat into the React Query detail cache immediately so
    // useChat / useActiveChat show it without waiting for a list refetch.
    seedChatDetail(chat);

    // Initialize state for the new chat
    get().initChatState(chat);

    // Add optimistic user message immediately so the UI shows it right away
    // This prevents the race condition where the chat renders before messages load
    // The real message will replace this when it arrives via gRPC stream or loadMessages()
    if (firstMessage) {
      const optimisticAttachments = getAttachmentsFromStore(
        attachmentIds || [],
      );
      const optimisticUserMessage: Message = {
        id: `optimistic-user-${Date.now()}`,
        chatId: "",
        role: MessageRole.USER,
        contentBlocks: [{ id: "", index: 0, type: ContentBlockType.TEXT, content: firstMessage }],
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        streamingState: StreamingState.COMPLETE,
        ordinal: BigInt(999998), // Just before streaming message (999999)
        thread: "",
        sequenceNumber: BigInt(0),
        attachments:
          optimisticAttachments.length > 0 ? optimisticAttachments : [],
      };

      // Seed the optimistic user message into the RQ message cache (the single
      // source of truth) so the UI shows it immediately.
      setMessagesInCache(chatId, [optimisticUserMessage]);
    }

    // Optimistically mark as RUNNING so the thinking indicator shows immediately
    // The backend will confirm via CHAT_ACTIVITY_CHANGED event shortly
    useActivityStore.getState().setActivity(chatId, ChatActivity.RUNNING);

    trackEvent("message_sent", {
      chatId,
      contentLength: firstMessage.length,
      hasAttachments: (attachmentIds?.length ?? 0) > 0,
      isFirstInChat: true,
    });

    return chat;
  },

  // Initialize state for a chat
  initChatState: (chat: Chat) => {
    const chatId = chat.id;

    // Skip if already initialized — presence of a message-list cache entry for
    // this chat is the init marker (created here and preserved thereafter).
    if (hasMessagesCache(chatId)) {
      // Still ensure the Chat object is homed below.
      seedChatDetail(chat);
      return;
    }

    // Seed the RQ message cache as the per-chat init marker (empty list).
    setMessagesInCache(chatId, []);

    // Use callback form of set() to ensure we're working with fresh state
    // This prevents race conditions when multiple state updates happen in sequence
    set((state) => ({
      toolCallStates: { ...state.toolCallStates, [chatId]: new Map() },
      toolResultsByCallId: { ...state.toolResultsByCallId, [chatId]: {} },
    }));

    // Home the Chat object into the React Query detail cache (the single source
    // of truth read by useChat / useActiveChat).
    seedChatDetail(chat);

    // Activity is NOT synced here. The authoritative paths are:
    // 1. loadChats (merge into activityStore on initial load / project switch)
    // 2. CHAT_ACTIVITY_CHANGED real-time events (globalUpdatesStore)
    // 3. Optimistic updates from user actions (cancel/pause/resume/create)
    // Writing activity from a potentially stale chat object would overwrite
    // correct real-time values.
  },

  // Set the active chat
  setActiveChat: (chatId: string | null) => {
    set({ activeChatId: chatId });
  },

  // Select a chat (make it active and load its data)
  selectChat: (chat: Chat) => {
    // Start profiler session here since this is the reliable entry point for tab switching
    tabSwitchProfiler.startSession(chat.id);

    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("selectChat-enter", {
        chatId: chat.id,
        messageCount: getMessagesFromCache(chat.id).length,
        hasMessages: getMessagesFromCache(chat.id).length > 0,
      });
    }

    const { activeChatId } = get();

    // If already selected, don't reload
    if (activeChatId === chat.id) {
      return;
    }

    // Check if this is a new chat or an already-initialized one. Presence of a
    // message-list cache entry is the per-chat init marker (see initChatState).
    const isNewChat = !hasMessagesCache(chat.id);

    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("selectChat-check-state", { isNewChat });
    }

    // Initialize state if needed
    if (isNewChat) {
      if (tabSwitchProfiler.isEnabled()) {
        tabSwitchProfiler.mark("initChatState-start");
      }
      get().initChatState(chat);
      if (tabSwitchProfiler.isEnabled()) {
        tabSwitchProfiler.mark("initChatState-end");
      }
    } else {
      // NOTE: auto_approve and is_planning_mode are now local state only (removed from backend)
      // Mode is tracked in chatParamsStore, not on chat object.
      // Refresh the React Query detail cache with the selected chat object.
      seedChatDetail(chat);
    }

    // Set as active and clear pending new chat worktree
    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("set-activeChatId");
    }

    set({
      activeChatId: chat.id,
    });

    // NOTE: viewerStore no longer has its own worktreeId state
    // The worktree context is managed by worktreeStore (single source of truth)
    // Browser viewers are filtered based on worktreeStore.currentWorktree?.id

    // Save to workspace state for persistence
    // IMPORTANT: Always use the current worktree context, not the chat's worktree_id
    // This ensures state is saved to where the user is currently viewing (e.g., __main__)
    // so it can be properly restored on reload
    const projectId = useProjectStore.getState().currentProject?.id;
    const currentWorktreeId =
      useWorktreeStore.getState().currentWorktree?.id ?? null;
    if (projectId) {
      useWorkspaceStateStore
        .getState()
        .setActiveChatId(projectId, currentWorktreeId, chat.id);
      useWorkspaceStateStore
        .getState()
        .addToChatQueue(projectId, currentWorktreeId, chat.id);
    }

    // New chats already had their empty message-list cache seeded by
    // initChatState above (the init marker); nothing more to do here.
    if (!isNewChat) {
      // For existing chats with cached data, preserve messages and approvals
      // Activity state is managed by activityStore (populated by server events)
      // They will be refreshed by the async loads below
      if (tabSwitchProfiler.isEnabled()) {
        const chatId = chat.id;
        const state = get();
        tabSwitchProfiler.mark("existing-chat-reuse-state", {
          messagesCount: getMessagesFromCache(chatId).length,
          toolResultsCount: Object.keys(
            state.toolResultsByCallId[chatId] || {},
          ).length,
        });
      }
    }

    // All async operations below are fire-and-forget (non-blocking)
    // Tab switching should be instant - data loads in background

    // Note: Agent preferences are loaded when user selects an agent in the UI
    // Agent is now a workflow param, not stored on chat

    // Dismiss unread state when viewing a chat (fire-and-forget)
    // Skipped if not unread, global WebSocket will update our state
    void get().dismissChat(chat.id);

    // If the chat appears to be running (busy), verify with backend and reconcile
    // This handles the case where DB says "running" but Temporal workflow is lost
    // The backend GetChat will detect this and mark needs_recovery=true
    const currentActivity = useActivityStore.getState().activities.get(chat.id) ?? ChatActivity.IDLE;
    if (currentActivity === ChatActivity.RUNNING || currentActivity === ChatActivity.AWAITING_INPUT || chat.needsRecovery) {
      logger.info(
        "[selectChat] Chat appears busy - checking status with backend",
        {
          chatId: chat.id.slice(0, 8),
          activity: currentActivity,
          needsRecovery: chat.needsRecovery,
        },
      );
      void get().checkChatStatus(chat.id);
    }

    // Start WebSocket for real-time updates (synchronous setup, async connection)
    // Subscribe to per-chat detail events via the unified global stream
    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("subscribeToChatDetails-call");
    }
    getGlobalUpdatesStore()?.subscribeToChatDetails(chat.id);
    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("subscribeToChatDetails-end");
    }

    // Load plan and tasks for this chat (fire-and-forget, cached)
    void (async () => {
      try {
        const { useTasksStore } = await import("./tasksStore");
        const { loadPlanAndTasks } = useTasksStore.getState();
        await loadPlanAndTasks(chat.id);
      } catch (error) {
        logger.error("[ChatStore] Failed to load plan/tasks:", error);
        Sentry.captureMessage('Failed to load plan and tasks', {
          level: 'warning',
          tags: { component: 'chat', operation: 'load_plan_tasks' },
        });
      }
    })();

    // Pending approvals and the pending question are server data owned by the
    // React Query cache: useApprovals / usePendingApprovals / usePendingQuestion
    // fetch them on mount and the chat stream patches the cache live (see
    // approval-queries.ts). No store-side prefetch needed here.

    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("selectChat-end");
    }
  },

  // Clear the current chat selection
  clearCurrentChat: (worktreeId?: string | null) => {
    logger.log(
      "📊 Clearing current chat",
      worktreeId ? `for worktree ${worktreeId}` : "",
    );
    const { activeChatId } = get();

    // Determine the target worktree context
    const targetWorktreeId =
      worktreeId ?? useWorktreeStore.getState().currentWorktree?.id ?? null;

    // NOTE: viewerStore no longer has its own worktreeId state
    // The worktree context is managed by worktreeStore (single source of truth)

    if (activeChatId) {
      // Don't clean up the state, just deselect
      // State cleanup happens when chat is deleted or after timeout
      set({ activeChatId: null });

      // Stop polling per-chat events for the deselected chat
      getGlobalUpdatesStore()?.unsubscribeFromChatDetails();

      // Clear from workspace state
      const projectId = useProjectStore.getState().currentProject?.id;
      if (projectId) {
        useWorkspaceStateStore
          .getState()
          .setActiveChatId(projectId, targetWorktreeId, null);
      }
    }
  },

  // Send a message to a specific chat
  sendMessage: async (
    chatId: string,
    content: string,
    attachmentIds?: string[],
    options?: {
      workflow?: string | null;
      workflowParams?: Record<string, unknown>;
      targetThread?: string | null;
      selectedPresets?: Record<string, string>;
      systemMessages?: Array<{ role: "system"; content: string }>; // System messages to prepend
      discuss?: boolean;
    },
  ) => {
    if (!getChatFromCache(chatId)) {
      throw new Error(`No state for chat ${chatId}`);
    }

    try {
      logger.info("[sendMessage] 📤 Sending message", {
        chatId: chatId.slice(0, 8),
        content: content.slice(0, 50),
        workflow: options?.workflow,
      });

      // DON'T set busy=true optimistically - let WebSocket workflow_execution updates drive busy state
      // This prevents stuck state if backend crashes before creating workflow_execution

      // Create optimistic user message so streaming response appears in the correct layer
      // Uses ordinal 999998 (just before streaming's 999999) and a stable temp ID
      // This will be replaced by the real message when it arrives via gRPC stream
      const optimisticAttachments = getAttachmentsFromStore(
        attachmentIds || [],
      );
      const optimisticUserMessage: Message = {
        id: `optimistic-user-${Date.now()}`,
        chatId: "",
        role: MessageRole.USER,
        contentBlocks: [{ id: "", index: 0, type: ContentBlockType.TEXT, content }],
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        streamingState: StreamingState.COMPLETE,
        ordinal: BigInt(999998), // Just before streaming message (999999)
        thread: "",
        sequenceNumber: BigInt(0),
        attachments:
          optimisticAttachments.length > 0 ? optimisticAttachments : [],
      };

      // Append the optimistic user message to the RQ message cache (the single
      // source of truth) to ensure correct layer grouping. It is replaced by
      // the real message when it arrives via the gRPC stream (matched/removed
      // by the "optimistic-user-" id prefix in processChatStreamUpdates).
      patchMessagesCache(chatId, (msgs) => [...msgs, optimisticUserMessage]);
      // Bump the chat timestamp when a message is sent, in the React Query
      // DETAIL cache only (projectId omitted → list untouched). The sidebar
      // renders from the RQ list, so patching the list would newly reorder
      // chats on send. Detail is the only surface a useChat consumer observes.
      patchChatCaches(undefined, chatId, {
        updatedAt: new Date().toISOString(),
      });

      // Use V2 message endpoint (no project ID required)
      // Model is now configured via workflow params or presets, not global preferences
      const workflowParams = options?.workflowParams || {};

      const isDiscuss = options?.discuss || get().discussMode[chatId];

      const sendOptions = {
        ...(options?.workflow !== undefined && { workflow: options.workflow }),
        ...(Object.keys(workflowParams).length > 0 && {
          workflow_params: workflowParams,
        }),
        ...(options?.targetThread && { target_thread: options.targetThread }),
        ...(options?.selectedPresets &&
          Object.keys(options.selectedPresets).length > 0 && {
            selected_presets: options.selectedPresets,
          }),
        ...(options?.systemMessages &&
          options.systemMessages.length > 0 && {
            systemMessages: options.systemMessages,
          }),
        ...(isDiscuss && { discuss: true }),
      };

      const response = await api.chatsV2.sendMessage(
        chatId,
        content,
        attachmentIds,
        Object.keys(sendOptions).length > 0 ? sendOptions : undefined,
      );

      logger.log("Message sent successfully:", response);

      const existingMessages = getMessagesFromCache(chatId);
      const isFirstInChat = existingMessages.filter(
        (m) => m.role === MessageRole.USER && !m.id.startsWith("optimistic-"),
      ).length === 0;
      trackEvent("message_sent", {
        chatId,
        contentLength: content.length,
        hasAttachments: (attachmentIds?.length ?? 0) > 0,
        isFirstInChat,
      });

      // Note: Optimistic user message will be replaced when the real message arrives via WebSocket
      // The hasRealUserMessage check in the WebSocket handler handles this automatically

      // Optimistically update chat workflow_name if workflow changed
      // Note: Agent is now a workflow param, not stored on chat - no optimistic update needed
      if (options?.workflow !== undefined && options.workflow) {
        // Patch the RQ caches (list + detail) — workflowName is surfaced in the
        // sidebar and ChatInput, matching handleChatConfigChanged.
        patchChatCaches(
          useProjectStore.getState().currentProject?.id,
          chatId,
          { workflowName: options.workflow ?? undefined }
        );
      }

      // Update workflow metadata from response so Temporal links stay current.
      // Patch the RQ DETAIL cache only — these are detail-level links, never
      // rendered in the sidebar list.
      if (response.workflowId || response.runId) {
        patchChatCaches(undefined, chatId, {
          ...(response.workflowId ? { workflowId: response.workflowId } : {}),
          ...(response.runId ? { runId: response.runId } : {}),
        });
      }

      // WebSocket will handle workflow status updates - no polling needed
    } catch (error) {
      logger.error("Failed to send message:", error);
      throw error;
    }
  },

  // Load recent messages for a chat (paginated from the end)
  // Loads only the most recent 100 messages for fast initial load
  loadMessages: async (chatId: string) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) return;

    try {
      // Load ALL messages via gRPC (no pagination limit)
      logger.debug(
        `[ChatStore] Loading messages for chat ${chatId.slice(0, 8)}`,
      );
      const response = await api.chatsV2.listMessages(chatId);
      // Canonical order — the server's flat list is sorted by raw ordinal,
      // which interleaves threads incorrectly (ordinals are per-thread).
      const messages = sortMessagesForDisplay(
        (response.messages || []).map(normalizeMessageAttachmentFields),
        chatId,
      );

      logger.debug(`[ChatStore] gRPC returned ${messages.length} messages`, {
        total: response.total,
        firstMessage: messages[0]
          ? {
              id: messages[0].id?.slice(0, 8),
              role: messages[0].role,
              ordinal: messages[0].ordinal,
              thread: messages[0].thread,
            }
          : null,
        lastMessage: messages[messages.length - 1]
          ? {
              id: messages[messages.length - 1].id?.slice(0, 8),
              role: messages[messages.length - 1].role,
              ordinal: messages[messages.length - 1].ordinal,
              thread: messages[messages.length - 1].thread,
            }
          : null,
      });

      const state = get();

      // PERFORMANCE: Pre-process all messages for fast rendering on tab switches
      // This is critical - without this, each ChatMessage parses JSON on every render
      // which causes 800ms+ delays when switching to chats with many messages
      const approvals =
        queryClient.getQueryData<ToolApprovalRequest[]>(
          approvalKeys.list(chatId),
        ) ?? [];
      // Build the normalized tool-result index from the loaded TOOL messages,
      // merged over anything already known for this chat, so processMessage can
      // resolve call→result the same way the live stream path does. (Loaded
      // assistant messages may also carry a backend-embedded matchedResult;
      // processMessage falls back to that when the index has no entry.)
      const loadedToolResults: ToolResultsByCallId = {
        ...(state.toolResultsByCallId[chatId] || {}),
      };
      for (const message of messages) {
        if (message.role !== MessageRole.TOOL) continue;
        for (const block of message.contentBlocks || []) {
          if (block.type === ContentBlockType.TOOL_RESULT && block.toolCallId) {
            loadedToolResults[block.toolCallId] = {
              content: block.content || "",
              is_error: block.isError,
              tool_name: block.toolName,
            };
          }
        }
      }

      // Warm the reference-keyed processMessage memo so the first render after
      // a tab switch reads cached parses instead of reparsing every message —
      // the performance reason this pre-pass exists. The memo (not a store
      // field) is now the parsed cache.
      for (const message of messages) {
        getProcessedMessage(message, loadedToolResults, approvals);
      }

      logger.info(
        `[ChatStore] Pre-processed ${messages.length} messages for fast rendering`,
        {
          chatId: chatId.slice(0, 8),
          loadedCount: messages.length,
        },
      );

      // Seed/replace the RQ message cache (the single source of truth) with the
      // loaded list; the tool-result index stays in Zustand.
      setMessagesInCache(chatId, messages);
      set({
        toolResultsByCallId: {
          ...state.toolResultsByCallId,
          [chatId]: loadedToolResults,
        },
      });
    } catch (error) {
      logger.error("[ChatStore] Failed to load messages:", error);
      Sentry.captureMessage('Failed to load chat messages', {
        level: 'warning',
        tags: { component: 'chat', operation: 'load_messages' },
        extra: { error: String(error), chatId },
      });
    }
  },


  // Process chat detail events from the unified global stream.
  // Called by globalUpdatesStore when per-chat events arrive.
  processChatStreamUpdates: (chatId: string, updates: ChatUpdate[], isSnapshot = false) => {
    // Helper to process streaming deltas and build/update a temporary streaming message
    const processStreamingDeltas = (
      deltas: StreamingDelta[],
      currentMsg: Message | null,
    ): Message | null => {
      let contentBlocks: Array<ContentBlock & { status?: string }> = [];
      let streamCancelled = false;

      let thread = currentMsg?.thread;
      for (const delta of deltas) {
        if (delta.thread) {
          thread = delta.thread;
          break;
        }
      }

      // Delta identity: all deltas in a thread group carry the same
      // pre-allocated message id. Capture it so the placeholder can adopt the
      // real id instead of a fabricated streaming-temp-* one.
      let deltaMessageId: string | undefined;
      for (const delta of deltas) {
        if (delta.message_id) {
          deltaMessageId = delta.message_id;
          break;
        }
      }

      if (currentMsg && currentMsg.contentBlocks) {
        contentBlocks = [...currentMsg.contentBlocks] as Array<ContentBlock & { status?: string }>;
      }

      for (const delta of deltas) {
        if (delta.delta_type === "message_start") {
          // A CallLLM retry re-streams the same message id from block 0. Reset
          // the placeholder's blocks so stale attempt-N content doesn't linger
          // beside the fresh attempt's blocks.
          contentBlocks = [];
          continue;
        }

        if (
          delta.delta_type === "thinking_block_start" ||
          delta.delta_type === "thinking_block_delta"
        ) {
          continue;
        }

        if (delta.delta_type === "content_block_start") {
          contentBlocks[delta.block_index] = {
            id: "",
            type: ContentBlockType.TEXT,
            index: delta.block_index,
            content: "",
          };
        } else if (delta.delta_type === "content_block_delta") {
          if (contentBlocks[delta.block_index]) {
            const block = contentBlocks[delta.block_index];
            contentBlocks[delta.block_index] = {
              ...block,
              content: (block.content || "") + (delta.delta || ""),
            };
          }
        } else if (delta.delta_type === "tool_use_start") {
          if (delta.tool_call) {
            contentBlocks[delta.block_index] = {
              id: "",
              type: ContentBlockType.TOOL_CALL,
              index: delta.block_index,
              toolCallId: delta.tool_call.id,
              toolName: delta.tool_call.name,
            };
          }
        } else if (delta.delta_type === "tool_use_stop") {
          // Tool call complete - full input will come in the complete message
        } else if (delta.delta_type === "tool_cancelled") {
          if (delta.tool_call) {
            const existingBlock = contentBlocks[delta.block_index];
            if (existingBlock && existingBlock.type === ContentBlockType.TOOL_CALL) {
              (existingBlock as ContentBlock & { status?: string }).status = "cancelled";
            } else {
              contentBlocks[delta.block_index] = {
                id: "",
                type: ContentBlockType.TOOL_CALL,
                index: delta.block_index,
                toolCallId: delta.tool_call.id,
                toolName: delta.tool_call.name,
                status: "cancelled",
              } as ContentBlock & { status?: string };
            }
          }
        } else if (delta.delta_type === "stream_cancelled") {
          logger.debug(
            "[Streaming] Stream cancelled, marking tools as cancelled",
          );
          streamCancelled = true;
          for (const block of contentBlocks) {
            if (block && block.type === ContentBlockType.TOOL_CALL && !(block as ContentBlock & { status?: string }).status) {
              (block as ContentBlock & { status?: string }).status = "cancelled";
            }
          }
        }
      }

      const compactBlocks = contentBlocks.filter(
        (block: unknown) => block !== undefined && block !== null,
      );

      // A tail of deltas from an already-finalized stream (deltas race the
      // final message on a separate server channel) must not fabricate an
      // empty placeholder: without a block-starting delta and without an
      // existing streaming message there is nothing to display.
      if (!currentMsg && compactBlocks.length === 0) {
        return null;
      }

      const normalizedThread = normalizeThreadKey(chatId, thread);
      // Placeholder identity: prefer the server's pre-allocated message id, then
      // an existing placeholder's id, then the legacy thread-keyed sentinel.
      // With a real id present the fabricated streaming-temp-* id — and the
      // phantom it enabled — is gone.
      const streamingId =
        deltaMessageId || currentMsg?.id || `streaming-temp-${normalizedThread}`;

      const streamingMsg: Message = {
        id: streamingId,
        chatId: currentMsg?.chatId || "",
        role: MessageRole.ASSISTANT,
        contentBlocks: compactBlocks,
        createdAt: currentMsg?.createdAt || new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        streamingState: streamCancelled ? StreamingState.COMPLETE : StreamingState.STREAMING,
        ordinal: currentMsg?.ordinal ?? BigInt(999999),
        thread: thread || "",
        sequenceNumber: BigInt(0),
        attachments: currentMsg?.attachments || [],
      };

      return streamingMsg;
    };

    // --- Begin processing updates (delivered from globalUpdatesStore) ---

    // Process all updates as received - if there are duplicates or ordering issues,
    // that's a backend bug that should be fixed rather than masked with client-side logic
    // Proto messages come wrapped in ProtoMessageUpdate from sync snapshots
    // Extract the inner proto Message objects
        const rawMessageUpdates = updates
          .filter((u): u is ProtoMessageUpdate => u.update_type === "message" && "message" in u)
          .map((u) => u.message);

        // GUARD: Filter out messages that don't belong to this chat.
        // This prevents cross-chat contamination if the stream delivers
        // stale messages from a previously-subscribed chat.
        const messageUpdates = rawMessageUpdates.filter((msg) => {
          if (msg.chatId && msg.chatId !== chatId) {
            logger.warn("[ChatStore] Dropping message from wrong chat", {
              expectedChatId: chatId.slice(0, 8),
              actualChatId: msg.chatId.slice(0, 8),
              messageId: msg.id?.slice(0, 8),
            });
            return false;
          }
          return true;
        });

        const approvalUpdates = updates.filter(
          (u): u is ToolApprovalUpdate => u.update_type === "approval",
        );
        const threadUpdates = updates.filter(
          (u): u is ActiveThreadUpdate => u.update_type === "thread",
        );
        const workflowStatusUpdates = updates.filter(
          (u): u is WorkflowStatusUpdate => u.update_type === "workflow_status",
        );
        const errorUpdates = updates.filter(
          (u): u is ErrorUpdate => u.update_type === "error",
        );
        const infoUpdates = updates.filter(
          (u): u is InfoUpdate =>
            u.update_type === "info" || u.update_type === "warning",
        );
        const runOutputUpdates = updates.filter(
          (u): u is RunOutputUpdate => u.update_type === "run_output",
        );
        const nodeExecutionUpdates = updates.filter(
          (u): u is NodeExecutionUpdate => u.update_type === "node_execution",
        );
        // Handle refetch signals from the chat stream (e.g., workflow_executions, plan_tasks)
        // After convertChatUpdateData, the JSON data is spread into the update object,
        // so { type: "workflow_executions" } becomes a top-level field.
        for (const u of updates) {
          if (u.update_type === "refetch") {
            const refetchType = (u as unknown as { type: string }).type as RefetchType;
            if (refetchType) {
              triggerRefetch(refetchType, chatId);
            }
          }
        }

        // Extract question updates
        const questionUpdates = updates.filter(
          (u): u is QuestionUpdate => u.update_type === "question",
        );

        // Extract tool_call updates directly from the updates array
        // FIXED in migration 038: Tool calls now come as update_type='tool_call'
        // Previously they were incorrectly emitted as update_type='message' and
        // were duplicated in message content_blocks. Now they come only once.
        const toolCallUpdates = updates.filter((u) => {
          return u.update_type === "tool_call";
        }) as ToolCallUpdate[];

        // Extract streaming_delta updates (real-time LLM output)
        // These are sent as the LLM generates content, before complete messages are saved
        const rawStreamingDeltas = updates.filter(
          (u): u is StreamingDelta => u.update_type === "streaming_delta",
        );

        // Extract stream_finalized markers (delta identity protocol). Emitted
        // exactly once per allocated assistant message id when its stream ends
        // (success/abort/cancel), riding the persisted+sequenced channel.
        const streamFinalizedUpdates = updates.filter(
          (u): u is StreamFinalizedUpdate => u.update_type === "stream_finalized",
        );

        // Ids finalized by THIS batch's markers — not yet committed to state, so
        // union them into the drop check for the same-batch marker+tail case.
        const batchFinalizedIds = new Set<string>();
        for (const u of streamFinalizedUpdates) {
          if (u.message_id) batchFinalizedIds.add(u.message_id);
        }

        // Delta identity: an assistant message id is "finalized" once its stream
        // ended. Reads live state each call (so the buffered-flush path sees a
        // finalization that landed after buffering), plus a persisted-message
        // backstop for old-server skew. Deltas without a message_id never match.
        const isFinalizedId = (id: string | undefined): boolean => {
          if (!id) return false;
          if (get().finalizedStreamIds[chatId]?.has(id)) return true;
          return getMessagesFromCache(chatId).some(
            (m) =>
              m.id === id &&
              m.role === MessageRole.ASSISTANT &&
              isProtoMessageComplete(m),
          );
        };

        // THE DROP RULE: a delta stamped with a finalized id is a stale tail —
        // drop it BEFORE buffering so it can never fabricate a placeholder. This
        // is what makes the phantom-at-end-of-chat impossible when message_id is
        // present. Id-less deltas (old servers) pass through to the legacy path.
        const liveStreamingDeltas = rawStreamingDeltas.filter(
          (d) =>
            !(
              isFinalizedId(d.message_id) ||
              (d.message_id ? batchFinalizedIds.has(d.message_id) : false)
            ),
        );

        // Buffer streaming deltas and flush on newlines to reduce re-renders
        // This batches char-by-char content_block_delta events until we hit a newline
        // Thread-aware: each thread has its own buffer and streaming message
        const handleBufferedFlush = (
          bufferedDeltas: StreamingDelta[],
          thread: string | undefined,
        ) => {
          // PERFORMANCE: Use requestAnimationFrame to batch state updates with React's render cycle
          // This ensures the timeout-triggered flush doesn't cause an extra render
          requestAnimationFrame(() => {
            // FIX 1: Guard against stale flushes - skip if buffer was cleared (complete message arrived)
            if (!hasStreamingBuffer(chatId, thread)) {
              logger.debug(
                "[Streaming] Skipping stale buffered flush - buffer was cleared",
                {
                  chatId: chatId.slice(0, 8),
                  thread: thread?.slice(0, 8),
                },
              );
              return;
            }

            // Delta identity: finalization can land between buffering and flush.
            // Re-apply the drop rule here so a marker (or persisted message) that
            // arrived in the interim retires the buffered tail instead of
            // flushing a placeholder for an already-finalized id.
            const flushDeltas = bufferedDeltas.filter(
              (d) => !isFinalizedId(d.message_id),
            );
            if (flushDeltas.length === 0) {
              logger.debug(
                "[Streaming] Skipping buffered flush - deltas finalized before flush",
                {
                  chatId: chatId.slice(0, 8),
                  thread: thread?.slice(0, 8),
                },
              );
              return;
            }

            // Process buffered deltas asynchronously (they were delayed by the buffer timeout)
            const threadKey = normalizeThreadKey(chatId, thread);
            const currentStreamingMsg = resolveStreamingBase(
              get().streamingMessages[chatId],
              threadKey,
            );

            const flushedMsg = processStreamingDeltas(
              flushDeltas,
              currentStreamingMsg,
            );
            if (flushedMsg) {
              set((s) => ({
                streamingMessages: {
                  ...s.streamingMessages,
                  [chatId]: {
                    ...(s.streamingMessages[chatId] || {}),
                    [threadKey]: flushedMsg,
                  },
                },
              }));
            }
          });
        };

        const streamingDeltas = bufferStreamingDeltas(
          chatId,
          liveStreamingDeltas,
          handleBufferedFlush,
        );

        // Convert ToolCallUpdate to ToolExecutionStateUpdate format
        // for internal state tracking (UI expects this format)
        const toolExecutionUpdates: ToolExecutionStateUpdate[] =
          toolCallUpdates.map((toolCall) => {
            const toolUpdate = {
              update_type: "tool_execution_state" as const,
              id: toolCall.content_block_id, // Use content_block_id as the ID!
              chat_id: chatId,
              tool_call_id: toolCall.content_block_id, // Map to content_block_id
              tool_name: toolCall.tool_name,
              status: toolCall.status,
              node_id: toolCall.node_id || "",
              sequence_number: toolCall.sequence_number || 0,
              timestamp:
                toolCall.requested_at ||
                toolCall.started_at ||
                toolCall.completed_at ||
                new Date().toISOString(),
            } as ToolExecutionStateUpdate;

            return toolUpdate;
          });

        // Also extract tool_cancelled events from streaming deltas
        // These represent streaming tool calls that were cancelled mid-stream
        // (they don't have content_block_ids, so we use the tool_call.id from the LLM)
        const cancelledToolUpdates: ToolExecutionStateUpdate[] = streamingDeltas
          .filter((d) => d.delta_type === "tool_cancelled" && d.tool_call)
          .map((delta) => ({
            update_type: "tool_execution_state" as const,
            id: delta.tool_call!.id, // Use LLM's tool_call ID
            chat_id: chatId,
            tool_call_id: delta.tool_call!.id,
            tool_name: delta.tool_call!.name,
            status: "cancelled" as const,
            node_id: "",
            sequence_number: delta.sequence_number || 0,
            timestamp: new Date().toISOString(),
          }));

        // Combine both types of tool execution updates
        const allToolExecutionUpdates = [
          ...toolExecutionUpdates,
          ...cancelledToolUpdates,
        ];

        // Use approval updates directly without conversion
        const allApprovals = approvalUpdates;

        // PERFORMANCE: Early return if we received ONLY streaming deltas that got fully buffered
        // This prevents triggering the expensive main set() call when there's nothing meaningful to update
        const hasNonStreamingUpdates =
          messageUpdates.length > 0 ||
          allApprovals.length > 0 ||
          threadUpdates.length > 0 ||
          workflowStatusUpdates.length > 0 ||
          errorUpdates.length > 0 ||
          infoUpdates.length > 0 ||
          runOutputUpdates.length > 0 ||
          nodeExecutionUpdates.length > 0 ||
          toolCallUpdates.length > 0 ||
          allToolExecutionUpdates.length > 0 ||
          // stream_finalized markers must reach the commit below so the
          // finalized-id set is seeded and any matching placeholder retired.
          streamFinalizedUpdates.length > 0;

        // If we only have raw streaming deltas and they all got buffered (no immediate deltas),
        // skip the main state update - the buffered flush will handle it later
        if (
          !hasNonStreamingUpdates &&
          rawStreamingDeltas.length > 0 &&
          streamingDeltas.length === 0
        ) {
          logger.debug(
            "[Streaming] All deltas buffered, skipping main state update",
            {
              chatId: chatId.slice(0, 8),
              bufferedCount: rawStreamingDeltas.length,
            },
          );
          return;
        }

        // Process streaming deltas to build temporary streaming messages per thread
        // These are applied to temporary messages that get replaced when complete messages arrive
        // NOTE: We don't call set() here - we'll include it in the main state update below
        const processedStreamingMsgs = new Map<string, Message>(); // threadKey -> Message
        if (streamingDeltas.length > 0) {
          logger.debug("[Streaming] Processing streaming deltas", {
            chatId: chatId.slice(0, 8),
            deltaCount: streamingDeltas.length,
            deltaTypes: streamingDeltas.map((d) => d.delta_type),
          });

          // Group deltas by thread for processing
          const deltasByThread = new Map<
            string | undefined,
            StreamingDelta[]
          >();
          for (const delta of streamingDeltas) {
            const thread = delta.thread;
            if (!deltasByThread.has(thread)) {
              deltasByThread.set(thread, []);
            }
            deltasByThread.get(thread)!.push(delta);
          }

          // Process each thread's deltas
          const chatStreaming = get().streamingMessages[chatId] || {};
          for (const [thread, threadDeltas] of deltasByThread) {
            const threadKey = normalizeThreadKey(chatId, thread);
            const currentStreamingMsg = resolveStreamingBase(
              chatStreaming,
              threadKey,
            );

            const processedMsg = processStreamingDeltas(
              threadDeltas,
              currentStreamingMsg,
            );

            if (processedMsg) {
              processedStreamingMsgs.set(threadKey, processedMsg);
              logger.debug("[Streaming] Updated streaming message for thread", {
                chatId: chatId.slice(0, 8),
                thread: threadKey.slice(0, 8),
                messageId: processedMsg.id,
                blockCount: processedMsg.contentBlocks?.length || 0,
              });
            }
          }
        }

        // Cross-match tool results with tool calls across all messages
        // First pass: collect all tool_results from tool messages in this
        // update batch into a normalized index keyed by tool_call_id. Tool
        // results arrive as separate TOOL-role messages; rather than embedding
        // them into the assistant message's TOOL_CALL blocks (a second join on
        // top of processMessage's internal one), we merge them into a per-chat
        // index and let processMessage resolve call→result at read time.
        // messageUpdates are proto Message objects (camelCase fields).
        const batchToolResults: ToolResultsByCallId = {};
        messageUpdates.forEach((protoMsg) => {
          if (protoMsg.role === MessageRole.TOOL) {
            protoMsg.contentBlocks.forEach((block) => {
              if (block.type === ContentBlockType.TOOL_RESULT && block.toolCallId) {
                batchToolResults[block.toolCallId] = {
                  content: block.content || "",
                  is_error: block.isError,
                  tool_name: block.toolName,
                };
              }
            });
          }
        });

        // Convert proto Message to frontend Message with string enum fields
        const convertProtoMsg = (protoMsg: any): Message => {
          const converted: Message = {
            ...protoMsg,
            role: typeof protoMsg.role === 'string' ? protoMsg.role : protoMsg.role,
            streamingState: typeof protoMsg.streamingState === 'string' ? protoMsg.streamingState : protoMsg.streamingState,
            displayStyle: protoMsg.displayStyle,
            attachments: normalizeMessageAttachments(protoMsg.attachments),
            contentBlocks: (protoMsg.contentBlocks || []).map((b: any) => ({
              ...b,
              type: typeof b.type === 'string' ? b.type : b.type,
            })),
          };
          return converted;
        };

        const messages: Message[] = messageUpdates.map(convertProtoMsg);

        // Threads whose stream finalized in this batch (a complete assistant
        // message arrived). Computed once here and captured by the set()
        // closure below; also used after set() to clear streaming buffers.
        const completedThreads = new Set<string>();
        messages.forEach((m) => {
          if (m.role === MessageRole.ASSISTANT && isProtoMessageComplete(m)) {
            completedThreads.add(normalizeThreadKey(chatId, m.thread));
          }
        });

        // Delta identity: assistant message ids finalized by THIS batch, from
        // both channels — persisted stream_finalized markers and every complete
        // persisted assistant message (redundancy + old-server skew).
        const newlyFinalizedIds = new Set<string>(batchFinalizedIds);
        messages.forEach((m) => {
          if (m.role === MessageRole.ASSISTANT && isProtoMessageComplete(m) && m.id) {
            newlyFinalizedIds.add(m.id);
          }
        });

        // The Chat object is homed in the React Query cache, not here — a
        // snapshot arriving before the chat is loaded no longer needs a
        // fabricated placeholder. Messages are stored independently below;
        // the real Chat lands via the detail/list query.
        //
        // Everything below is computed synchronously (no awaits between this
        // read and the set() that commits, and batches are synchronous), then
        // committed in ONE set(), with the external-store / RQ-cache side
        // effects applied after the commit.
        const state = get();

        // Check if we received a complete assistant message (replaces streaming)
        const hasCompleteAssistantMessage = messages.some(
          (m) => m.role === MessageRole.ASSISTANT && isProtoMessageComplete(m),
        );

        // SNAPSHOT vs INCREMENTAL: snapshot replaces the list (cross-chat /
        // stale contamination guard), incremental upserts by id. Current
        // messages come from the RQ cache (single source of truth); reads are
        // synchronous and setQueryData commits synchronously, so this sees the
        // latest merged array — same freshness the old get()-based read had.
        if (isSnapshot) {
          logger.info("[ChatStore] Snapshot: replacing messages for chat", {
            chatId: chatId.slice(0, 8),
            snapshotCount: messages.length,
            previousCount: getMessagesFromCache(chatId).length,
          });
        }
        let updatedMessages = mergeMessages(
          getMessagesFromCache(chatId),
          messages,
          isSnapshot,
        );

        // Merge this batch's tool results into the per-chat normalized
        // index. Results arrive as their own TOOL messages, often in a later
        // batch than the assistant tool call — carrying them in a chat-level
        // index (rather than re-embedding into already-stored assistant
        // blocks) is what makes "late results" attach on the next read
        // without mutating the assistant message.
        const updatedToolResults: ToolResultsByCallId = {
          ...(isSnapshot ? {} : state.toolResultsByCallId[chatId] || {}),
          ...batchToolResults,
        };

        // Gather all streaming messages: newly processed ones merged with existing ones
        // Thread-aware: each thread can have its own streaming message
        const existingChatStreaming =
          state.streamingMessages[chatId] || {};
        const mergedStreamingMsgs = new Map<string, Message>();

        // Start with existing streaming messages
        for (const [threadKey, msg] of Object.entries(
          existingChatStreaming,
        )) {
          if (msg) {
            mergedStreamingMsgs.set(threadKey, msg);
          }
        }

        // Override with newly processed streaming messages
        for (const [threadKey, msg] of processedStreamingMsgs) {
          mergedStreamingMsgs.set(threadKey, msg);
        }

        // completedThreads (threads finalized in this batch) is computed
        // once above and captured here.
        // If we received complete messages, handle streaming cleanup for those threads
        if (completedThreads.size > 0) {
          logger.debug(
            "[Streaming] Complete messages received, cleaning up streaming for threads",
            {
              chatId: chatId.slice(0, 8),
              completedThreads: [...completedThreads].map((t) =>
                t.slice(0, 8),
              ),
            },
          );

          // For each completed thread, handle cancelled tool preservation and remove streaming
          for (const threadKey of completedThreads) {
            // The slice only ever holds the ephemeral placeholder for this
            // thread; preserve any tool calls it captured as cancelled.
            const streamingMsg = mergedStreamingMsgs.get(threadKey);
            if (streamingMsg) {
              const streamingBlocks = streamingMsg.contentBlocks || [];
              const cancelledBlocks = streamingBlocks.filter(
                (block: ContentBlock & { status?: string }) =>
                  block.type === ContentBlockType.TOOL_CALL &&
                  block.status === "cancelled",
              );

              if (cancelledBlocks.length > 0) {
                logger.debug(
                  "[Streaming] Preserving cancelled tool calls from streaming message",
                  {
                    chatId: chatId.slice(0, 8),
                    thread: threadKey.slice(0, 8),
                    cancelledCount: cancelledBlocks.length,
                  },
                );

                // Find the complete assistant message for this thread and merge cancelled blocks
                updatedMessages = updatedMessages.map((msg) => {
                  const msgThreadKey = normalizeThreadKey(
                    chatId,
                    msg.thread,
                  );
                  if (
                    msg.role === MessageRole.ASSISTANT &&
                    isProtoMessageComplete(msg) &&
                    msgThreadKey === threadKey
                  ) {
                    const existingIds = new Set(
                      (msg.contentBlocks || []).map((b) => b.id),
                    );
                    const newCancelledBlocks = cancelledBlocks.filter(
                      (cb: any) => !existingIds.has(cb.id),
                    );
                    if (newCancelledBlocks.length > 0) {
                      return {
                        ...msg,
                        contentBlocks: [
                          ...(msg.contentBlocks || []),
                          ...newCancelledBlocks,
                        ],
                      };
                    }
                  }
                  return msg;
                });
              }
            }

            // Remove streaming message for this completed thread
            mergedStreamingMsgs.delete(threadKey);
          }
        }

        // Delta identity: retire any placeholder whose id was finalized this
        // batch. The completedThreads path above only fires when a COMPLETE
        // assistant message arrives; a stream that finalized via an
        // aborted/cancelled marker (no persisted message) leaves the placeholder
        // behind unless we delete it by id here. Keyed by placeholder identity,
        // so it also catches a thread mismatch between marker and placeholder.
        if (newlyFinalizedIds.size > 0) {
          for (const [threadKey, msg] of [...mergedStreamingMsgs]) {
            if (msg && newlyFinalizedIds.has(msg.id)) {
              logger.debug("[Streaming] Retiring placeholder for finalized id", {
                chatId: chatId.slice(0, 8),
                thread: threadKey.slice(0, 8),
                messageId: msg.id,
              });
              mergedStreamingMsgs.delete(threadKey);
            }
          }
        }

        // The in-flight streaming placeholder lives ONLY in the
        // streamingMessages slice (written below), never in the persisted
        // messages array. The render layer composes the two at display time
        // (see ChatContainer / WorkflowBuilderChat). This keeps persisted
        // messages as server-truth and the ephemeral placeholder as the sole
        // owner of in-progress content — no double-storage, no
        // streaming-temp bookkeeping in the messages array.

        // Canonical order (oldest first): per-thread ordinal, threads
        // interleaved by clamped time — see lib/messageOrder.ts. createdAt
        // alone is NOT trustworthy (repair/attachment messages have shipped
        // with wrong timestamps; placeholders stamp the client clock).
        updatedMessages = sortMessagesForDisplay(updatedMessages, chatId);

        // Note: activity updates come through the global stream and are
        // handled by activityStore, so we don't need to fetch status separately
        // when threads complete.

        // Process error updates
        // Each error persists in the chat - no deduplication.
        // Errors are sorted by timestamp in the timeline so they appear
        // in chronological order alongside messages.
        // On snapshot: start fresh to avoid stale/duplicate events from previous subscription
        const updatedErrorEvents = applyErrorUpdates(
          state.errorEvents[chatId] || [],
          errorUpdates,
          isSnapshot,
        );
        // Route on any chat-error marker tail planted by the workflow /
        // driver (RELIANT_MANAGED_QUOTA_EXHAUSTED, RELIANT_DAEMON_OFFLINE_HALT,
        // …). Live-only side effects (modal pop) skip the snapshot path;
        // marker-stripping is idempotent and runs in both modes so the
        // rendered timeline is clean either way. Kept in the orchestrator
        // because it's a side effect, not part of the pure log reduction.
        errorUpdates.forEach((errorUpdate) =>
          routeChatErrorMarker(errorUpdate, isSnapshot),
        );

        const updatedInfoEvents = applyInfoUpdates(
          state.infoEvents[chatId] || [],
          infoUpdates,
          isSnapshot,
        );

        const updatedRunOutputs = applyRunOutputUpdates(
          state.runOutputs[chatId] || [],
          runOutputUpdates,
          isSnapshot,
        );

        const updatedNodeExecutions = applyNodeExecutionUpdates(
          state.nodeExecutions[chatId] || [],
          nodeExecutionUpdates,
          isSnapshot,
        );

        if (workflowStatusUpdates.length > 0) {
          logger.debug(
            "[WorkflowStatus] Chat stream updates received",
            {
              chatId: chatId.slice(0, 8),
              count: workflowStatusUpdates.length,
              statuses: workflowStatusUpdates.map((u) => u.status),
            },
          );
        }

        // Process tool execution state updates (dedup by tool_call_id,
        // guard terminal statuses).
        const updatedToolCallStates = applyToolCallStateUpdates(
          state.toolCallStates[chatId] || new Map(),
          allToolExecutionUpdates,
          chatId,
        );

        // NOTE: chat activity is NOT updated here.
        // It is handled by handleChatActivityChanged() in globalUpdatesStore.ts
        // via the global user update stream, which populates activityStore.
        // Activity state is read from activityStore (single source of truth).

        // Log state update details
        logger.debug("[Streaming] State update", {
          chatId: chatId.slice(0, 8),
          messageCount: updatedMessages.length,
          hasCompleteMessage: hasCompleteAssistantMessage,
          streamingMsgCount: processedStreamingMsgs.size,
          streamingThreads: [...processedStreamingMsgs.keys()].map((t) =>
            t.slice(0, 8),
          ),
        });

        // Pre-parse message content so the render layer reads from the
        // reference-keyed processMessage memo without reparsing, and drive
        // the real-time task side effects below. Rendering invalidation is
        // handled by the memo itself (keyed on message / tool-result index /
        // approvals references), so there is no store-side parsed cache to
        // keep in sync — we just warm the memo and run the side effects.
        const messagesToProcess = [
          ...messages, // New/updated messages from this WebSocket update
        ];

        // Also process streaming messages if present (all threads)
        for (const streamingMsg of processedStreamingMsgs.values()) {
          messagesToProcess.push(streamingMsg);
        }

        // CRITICAL (late results): a tool result usually arrives in a LATER
        // batch than the assistant tool call it belongs to. That assistant
        // message is already stored (not in this batch's `messages`). The
        // render memo picks up the new tool-result index reference on its
        // own, but the task side effects below only run for messages we
        // process here — so queue the already-stored assistant messages whose
        // calls this batch's results resolve, or a task tool's late result
        // would be missed.
        const batchResultCallIds = Object.keys(batchToolResults);
        if (batchResultCallIds.length > 0) {
          const alreadyQueued = new Set(messagesToProcess.map((m) => m.id));
          const resolvedIds = new Set(batchResultCallIds);
          for (const msg of updatedMessages) {
            if (msg.role !== MessageRole.ASSISTANT) continue;
            if (alreadyQueued.has(msg.id)) continue;
            const owns = (msg.contentBlocks || []).some(
              (block) =>
                block.type === ContentBlockType.TOOL_CALL &&
                block.toolCallId != null &&
                resolvedIds.has(block.toolCallId),
            );
            if (owns) {
              messagesToProcess.push(msg);
              alreadyQueued.add(msg.id);
            }
          }
        }

        const newState = {
          toolResultsByCallId: {
            ...state.toolResultsByCallId,
            [chatId]: updatedToolResults,
          },
          errorEvents: { ...state.errorEvents, [chatId]: updatedErrorEvents },
          infoEvents: { ...state.infoEvents, [chatId]: updatedInfoEvents },
          runOutputs: { ...state.runOutputs, [chatId]: updatedRunOutputs },
          nodeExecutions: {
            ...state.nodeExecutions,
            [chatId]: updatedNodeExecutions,
          },
          toolCallStates: {
            ...state.toolCallStates,
            [chatId]: updatedToolCallStates,
          },
          // NOTE: chat activity is NOT updated here.
          // It is handled by handleChatActivityChanged() in globalUpdatesStore.ts
          // via the global user update stream, which populates activityStore.
          // NOTE: contextUsage is NOT touched here — it arrives via
          // handleChatContextUsage (onContextUsage callback), not via
          // message updates.
          // Update streaming messages state (thread-aware):
          // - For completed threads: remove their streaming messages
          // - For active threads: update with newly processed streaming messages
          // Only update if there are actual changes (avoid creating new object references unnecessarily)
          streamingMessages: (() => {
            const hasStreamingChanges =
              processedStreamingMsgs.size > 0 ||
              completedThreads.size > 0 ||
              // A finalize marker with no persisted message can still retire a
              // placeholder by id — that mutates mergedStreamingMsgs too.
              newlyFinalizedIds.size > 0;
            if (!hasStreamingChanges) {
              // No streaming changes - preserve existing state reference
              return state.streamingMessages;
            }
            // Convert mergedStreamingMsgs Map to object for state
            const newChatStreaming = Object.fromEntries(mergedStreamingMsgs);
            return {
              ...state.streamingMessages,
              [chatId]:
                Object.keys(newChatStreaming).length > 0
                  ? newChatStreaming
                  : undefined,
            };
          })(),
          // Delta identity: seed the finalized-id set. Snapshot REPLACES it
          // (snapshot semantics — the snapshot dedups markers by entity_id, so
          // it carries one marker per finalized message); incremental UNIONS.
          finalizedStreamIds: (() => {
            if (newlyFinalizedIds.size === 0 && !isSnapshot) {
              return state.finalizedStreamIds;
            }
            const base = isSnapshot
              ? new Set<string>()
              : new Set(state.finalizedStreamIds[chatId] || []);
            for (const id of newlyFinalizedIds) base.add(id);
            return {
              ...state.finalizedStreamIds,
              [chatId]: base,
            };
          })(),
        };

        // Commit the Zustand slices in one shot. All inputs were computed
        // synchronously above from get() — nothing can have changed since.
        set(newState);

        // ---- External writes (post-commit) ----
        // Everything below writes to stores OTHER than this one (RQ caches,
        // threadActivityStore, tasksStore). They run after the Zustand commit
        // but in the same synchronous task, so React batches all of it into
        // one render — same observable ordering as before.

        // Commit the freshly-merged messages array to the RQ cache (the
        // single source of truth). snapshot-replace / incremental-upsert /
        // optimistic-user replacement / canonical sort were all applied to
        // `updatedMessages` above.
        setMessagesInCache(chatId, updatedMessages);

        // Approval updates from the stream patch the React Query cache
        // directly (setQueryData, no refetch) — approvals are server data
        // owned by the cache, read via useApprovals/usePendingApprovals.
        // usePendingApprovals derives "pending" from the same list via a
        // client-side select, so one list upsert updates both. The patch IS
        // the sync; no invalidate/refetch round-trip.
        allApprovals.forEach((approvalUpdate) => {
          const approval: ToolApprovalRequest = {
            id: approvalUpdate.id,
            chat_id: approvalUpdate.chat_id,
            content_block_id:
              approvalUpdate.content_block_id || approvalUpdate.entity_id,
            status: toApprovalStatus(approvalUpdate.status),
            denial_reason: approvalUpdate.denial_reason,
            created_at: approvalUpdate.created_at,
            responded_at: approvalUpdate.responded_at,
            action_taken: approvalUpdate.action_taken, // Which action was clicked
          };
          upsertApprovalInCache(chatId, approval);
        });

        // Question updates patch the React Query cache (server data), read
        // via usePendingQuestion. A "pending" event sets it; "resolved" clears.
        for (const qu of questionUpdates) {
          if (qu.status === "pending") {
            patchPendingQuestionCache(chatId, {
              question_id: qu.question_id,
              chat_id: qu.chat_id,
              workflow_id: qu.workflow_id,
              step_id: qu.step_id,
              status: qu.status,
              created_at: "",
              metadata: qu.metadata,
            });
          } else if (qu.status === "resolved") {
            patchPendingQuestionCache(chatId, null);
          }
        }

        // Thread updates live in threadActivityStore (not chatStore). Merge
        // is pure; this is the store write.
        if (threadUpdates.length > 0) {
          const threadActivityState = useThreadActivityStore.getState();
          threadActivityState.setThreads(
            chatId,
            mergeActiveThreads(
              threadActivityState.threads[chatId] || [],
              threadUpdates,
            ),
          );
        }

        // Pre-parse message content so the render layer reads from the
        // reference-keyed processMessage memo without reparsing, and drive
        // the real-time task side effects (tasksStore writes). Rendering
        // invalidation is handled by the memo itself (keyed on message /
        // tool-result index / approvals references), so there is no
        // store-side parsed cache to keep in sync.
        //
        // Approvals live in the React Query cache; read the current list
        // (including this batch's upserts above) to embed approval status
        // into processed tool calls.
        const approvalsForProcessing =
          queryClient.getQueryData<ToolApprovalRequest[]>(
            approvalKeys.list(chatId),
          ) ?? [];
        messagesToProcess.forEach((message) => {
          const processed = getProcessedMessage(
            message,
            updatedToolResults,
            approvalsForProcessing,
          );

          // Process task-related tool calls in real-time as messages arrive
          // This ensures tasks appear immediately in the sidebar without needing ChatMessage to render
          // Only process each tool result once (messages may be reprocessed on cross-matching)
          if (
            processed.toolExecutions &&
            processed.toolExecutions.length > 0
          ) {
            const tasksStore = useTasksStore.getState();
            processed.toolExecutions.forEach((exec) => {
              const toolCallId = exec.call?.id;
              if (!toolCallId) return;
              const toolName = exec.call?.name?.toLowerCase?.();
              const content = exec.result?.content;
              if (!content || typeof content !== "string") return;

              // Skip if we've already processed this tool result
              if (processedTaskToolCallIds.has(toolCallId)) return;

              if (toolName === "update_task") {
                processedTaskToolCallIds.add(toolCallId);
                tasksStore.processUpdateTaskContent(chatId, content);
              } else if (toolName === "add_task") {
                processedTaskToolCallIds.add(toolCallId);
                tasksStore.processAddTaskContent(chatId, content);
              } else if (toolName === "create_subtask") {
                processedTaskToolCallIds.add(toolCallId);
                tasksStore.processCreateSubtaskContent(chatId, content);
              } else if (toolName === "list_tasks") {
                processedTaskToolCallIds.add(toolCallId);
                tasksStore.processListTasksContent(chatId, content);
              } else if (toolName === "create_plan") {
                processedTaskToolCallIds.add(toolCallId);
                tasksStore.processCreatePlanContent(chatId, content);
              }
            });
          }
        });

        // CRITICAL FIX: Clear streaming buffers when complete messages arrive
        // This prevents buffer flush timeouts from firing after complete messages
        // and recreating partial streaming messages that overwrite complete ones
        // Thread-aware: only clear buffers for threads that received complete messages
        if (completedThreads.size > 0) {
          for (const threadKey of completedThreads) {
            clearStreamingBuffer(chatId, threadKey);
          }
          logger.debug(
            "[Streaming] Cleared streaming buffers for completed threads",
            {
              chatId: chatId.slice(0, 8),
              threads: [...completedThreads].map((t) =>
                t.slice(0, 8),
              ),
            },
          );
        }
  },


  // Remove the in-flight streaming placeholder and buffers for a chat. The
  // activity-IDLE event (persisted + gap-protected) is the authoritative
  // signal that no stream is running; any placeholder still present is a stale
  // delta tail that would otherwise render until a refresh. The placeholder
  // lives solely in the streamingMessages slice, so clearing it is a single
  // slice delete — no messages-array filtering.
  clearStreamingState: (chatId: string) => {
    // Drop buffers first so pending flush timers can't resurrect a placeholder.
    clearStreamingBuffersForChat(chatId);

    set((state) => {
      if (!state.streamingMessages[chatId]) {
        return state;
      }

      logger.info("[Streaming] Clearing streaming state for idle chat", {
        chatId: chatId.slice(0, 8),
      });

      const newStreamingMessages = { ...state.streamingMessages };
      delete newStreamingMessages[chatId];

      return { streamingMessages: newStreamingMessages };
    });
  },

  // Cancel chat - cancels all sessions in the chat
  cancelChat: async (chatId: string) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) {
      logger.warn("No project ID available for cancellation");
      return;
    }

    if (!getChatFromCache(chatId)) {
      logger.warn("No chat state found for chatId:", chatId);
      return;
    }

    // IMMEDIATELY update UI state for instant feedback (optimistic update)
    // NOTE: We do NOT clear thread activity here. The useIsThreadActive hook already
    // returns false when the chat is not RUNNING, so threads appear inactive.
    // Mark any incomplete messages as finished in the RQ message cache.
    patchMessagesCache(chatId, (msgs) =>
      msgs.map((msg) =>
        !isProtoMessageComplete(msg)
          ? { ...msg, streamingState: StreamingState.COMPLETE }
          : msg,
      ),
    );
    // Set the chat IDLE in the RQ caches (list + detail).
    patchChatCaches(projectId, chatId, { state: ChatState.IDLE });

    // Also update activityStore so sidebar dot reflects change immediately
    useActivityStore.getState().setActivity(chatId, ChatActivity.IDLE);

    // Then make the API call to actually cancel the workflow
    try {
      await api.chatsV2.cancel(chatId);
    } catch (error) {
      logger.error("Failed to cancel chat:", error);
      // UI state already updated optimistically, so no need to update again
      // The WebSocket will eventually send the actual cancelled status
    }
  },

  // Pause chat - pauses a running workflow
  pauseChat: async (chatId: string) => {
    if (!getChatFromCache(chatId)) {
      logger.warn("No chat state found for chatId:", chatId);
      return;
    }

    // IMMEDIATELY update UI state for instant feedback (optimistic update)
    // NOTE: We intentionally do NOT clear thread activity here. The useIsThreadActive
    // hook already returns false when the chat is not RUNNING, so threads
    // appear inactive regardless. On resume, the backend re-emits thread updates
    // to repopulate thread activity (handles multi-window and page refresh cases).
    // Mark any incomplete messages as finished in the RQ message cache.
    patchMessagesCache(chatId, (msgs) =>
      msgs.map((msg) =>
        !isProtoMessageComplete(msg)
          ? { ...msg, streamingState: StreamingState.COMPLETE }
          : msg,
      ),
    );
    // Set the chat IDLE in the RQ caches (list + detail).
    patchChatCaches(
      useProjectStore.getState().currentProject?.id,
      chatId,
      { state: ChatState.IDLE }
    );

    // Also update activityStore so sidebar dot reflects change immediately
    useActivityStore.getState().setActivity(chatId, ChatActivity.IDLE);

    // Make the API call to actually pause the workflow
    try {
      await api.chatsV2.pause(chatId);
    } catch (error) {
      logger.error("Failed to pause chat:", error);
      // Revert optimistic update on error
      get().refreshChat(chatId);
    }
  },

  setDiscussMode: (chatId, enabled) => {
    set((state) => ({
      discussMode: { ...state.discussMode, [chatId]: enabled },
    }));
  },

  // Resume chat - resumes a paused or expired workflow
  // For paused: backend sends SignalResume to the running Temporal workflow
  // For expired: backend uses ResetWorkflowExecution to restore it
  // The backend ResumeChat handler detects the state and handles both transparently
  resumeChat: async (chatId: string) => {
    if (!getChatFromCache(chatId)) {
      logger.warn("No chat state found for chatId:", chatId);
      return;
    }

    // Optimistic update — clear discuss mode since we're resuming. The chat
    // object itself is unchanged (activity is tracked in activityStore).
    set((state) => ({
      discussMode: { ...state.discussMode, [chatId]: false },
    }));

    // Also update activityStore so sidebar dot reflects change immediately
    useActivityStore.getState().setActivity(chatId, ChatActivity.RUNNING);

    // Make the API call to actually resume the workflow
    try {
      await api.chatsV2.resume(chatId);
    } catch (error) {
      logger.error("Failed to resume chat:", error);
      // Revert optimistic update on error
      get().refreshChat(chatId);
    }
  },

  resolveQuestion: async (chatId, questionId, action, responseData) => {
    await questionGrpc.resolveQuestion(questionId, action, responseData);
    // Optimistically clear the pending question in the React Query cache.
    patchPendingQuestionCache(chatId, null);
  },

  // Refresh chat - reconnects the unified stream to re-fetch data
  refreshChat: async (chatId: string) => {
    // Data refreshes via the unified global stream reconnection
    // Re-subscribing to the chat triggers a fresh sync snapshot
    getGlobalUpdatesStore()?.subscribeToChatDetails(chatId);
  },


  // Force reset a stuck chat to idle state
  // This is a recovery function for chats that are stuck in busy state
  // It clears all running state without making API calls
  forceResetChatToIdle: (chatId: string) => {
    logger.warn("⚠️ Force resetting chat to idle state (recovery mode):", {
      chatId: chatId.slice(0, 8),
    });

    // Clear thread activity in the dedicated store
    useThreadActivityStore.getState().clearThreads(chatId);
    // Approvals are server data in the RQ cache; reconcile from the server.
    queryClient.invalidateQueries({ queryKey: approvalKeys.list(chatId) });

    // Mark any incomplete messages as finished in the RQ message cache.
    patchMessagesCache(chatId, (msgs) =>
      msgs.map((msg) =>
        !isProtoMessageComplete(msg)
          ? { ...msg, streamingState: StreamingState.COMPLETE }
          : msg,
      ),
    );

    // Mirror the IDLE transition into the RQ caches (list + detail).
    patchChatCaches(
      useProjectStore.getState().currentProject?.id,
      chatId,
      { state: ChatState.IDLE }
    );

    // Also update activityStore so sidebar dot reflects change immediately
    useActivityStore.getState().setActivity(chatId, ChatActivity.IDLE);

    logger.info("✅ Chat reset to idle state", {
      chatId: chatId.slice(0, 8),
    });
  },

  // Check chat status
  checkChatStatus: async (chatId: string) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) return;

    try {
      // Capture the sequence baseline BEFORE the fetch: the GetChat response
      // reflects server state at least as fresh as everything the client had
      // processed at request time, so it may overwrite entries with seq <=
      // baseline but must yield to stream events that arrive mid-flight.
      const baselineSeq = useActivityStore.getState().maxSeenSeq;

      // Use the get endpoint to check if chat exists and get its state
      const chat = await api.chatsV2.get(chatId);

      // NOTE: auto_approve and is_planning_mode were removed from backend
      // Mode is tracked in chatParamsStore, not on chat object.
      // Home the fresh chat into the React Query detail cache.
      seedChatDetail(chat);

      // Sync activity from the fetched chat under sequence precedence. This
      // is the stuck-busy recovery path: a stale RUNNING left by a missed
      // IDLE event has seq <= baseline, so the snapshot reconciles it to
      // IDLE. A fresh optimistic write (tagged maxSeenSeq+1 > baseline)
      // stays protected from a snapshot that predates its workflow start.
      const serverActivity = (chat.activity ?? 0) as ChatActivity;
      useActivityStore
        .getState()
        .applyChatSnapshot(chatId, serverActivity, baselineSeq);
    } catch (error) {
      logger.error("Failed to check chat status:", error);
    }
  },

  // Branch chat - helper to handle post-branch navigation
  _navigateToBranchedChat: (newChat: Chat, _worktreeId?: string) => {
    // Navigate to the branched chat
    const { navigateToChat } = useChatNavigationStore.getState();
    navigateToChat(newChat.id);
  },

  branchChat: async (chatId: string, messageId: string) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) return;

    try {
      // Branch using message ID - the backend will look up the message directly
      // This works for both local messages and inherited messages from parent chats
      const { chat: newChat } = await api.chatsV2.branch(chatId, {
        messageId,
      });

      // Home the branched chat into the React Query detail cache immediately.
      seedChatDetail(newChat);

      // Initialize state for new chat
      get().initChatState(newChat);

      // Copy workflow params from source chat to branched chat
      const sourceParams = useChatParamsStore.getState().getChatParams(chatId);
      if (Object.keys(sourceParams).length > 0) {
        useChatParamsStore
          .getState()
          .setChatParams(newChat.id, sourceParams);
      }

      // Select the new chat
      get().selectChat(newChat);

      // Navigate to the branched chat
      get()._navigateToBranchedChat(newChat);
    } catch (error) {
      // Extract error message from ConnectError or other error types
      const errorMessage =
        error instanceof Error
          ? error.message
          : (error as { message?: string })?.message || JSON.stringify(error);
      logger.error("Failed to branch chat:", {
        error,
        errorMessage,
        errorType: error?.constructor?.name,
        errorString: String(error),
      });
      throw error;
    }
  },

  branchChatToWorktree: async (
    chatId: string,
    messageId: string,
    worktreeId: string,
    _workspaceContext?: {
      sourceWorktreeId?: string;
      filesCopied?: string[];
      copyFilesEnabled?: boolean;
    },
  ) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) return;

    try {
      // Branch using message ID - the backend will look up the message directly
      const { chat: newChat } = await api.chatsV2.branch(chatId, {
        messageId,
        worktreeId,
      });

      // Home the branched chat into the React Query detail cache immediately.
      seedChatDetail(newChat);

      // Initialize state for new chat
      get().initChatState(newChat);

      // Copy workflow params from source chat to branched chat
      const sourceParams = useChatParamsStore.getState().getChatParams(chatId);
      if (Object.keys(sourceParams).length > 0) {
        useChatParamsStore
          .getState()
          .setChatParams(newChat.id, sourceParams);
      }

      // Select the new chat
      get().selectChat(newChat);

      // Navigate to the branched chat
      get()._navigateToBranchedChat(newChat, worktreeId);
    } catch (error) {
      logger.error("Failed to branch chat to worktree:", error);
      throw error;
    }
  },

  // Connection restored - streaming is managed by globalUpdatesStore
  onConnectionRestored: () => {
    set({ error: null });
    // The unified global stream reconnects automatically via globalUpdatesStore
  },

  // Clear error
  clearError: () => {
    set({ error: null });
  },

  // Retry connection - streaming is managed by globalUpdatesStore
  retryConnection: async () => {
    const { activeChatId } = get();
    if (activeChatId) {
      getGlobalUpdatesStore()?.subscribeToChatDetails(activeChatId);
    }
  },

  // Tool call management methods
  updateToolCallState: (
    chatId: string,
    toolCallId: string,
    toolCall: ToolCallState,
  ) => {
    const state = get();
    const toolCalls = state.toolCallStates[chatId];
    if (toolCalls) {
      const newToolCalls = new Map(toolCalls);
      newToolCalls.set(toolCallId, toolCall);

      set({
        toolCallStates: { ...state.toolCallStates, [chatId]: newToolCalls },
      });
    }
  },

  // Enhanced method for partial updates to tool call state
  updateToolCallStatePartial: (
    chatId: string,
    toolCallId: string,
    updates: Partial<ToolCallState>,
  ) => {
    const state = get();
    const toolCalls = state.toolCallStates[chatId];
    if (toolCalls) {
      const newToolCalls = new Map(toolCalls);
      const existingToolCall = newToolCalls.get(toolCallId);

      // Merge with existing data or create new
      const updatedToolCall: ToolCallState = {
        id: toolCallId,
        sessionId: updates.sessionId || existingToolCall?.sessionId || "",
        toolName: updates.toolName || existingToolCall?.toolName || "",
        status: updates.status || existingToolCall?.status || "pending",
        timestamp:
          updates.timestamp ||
          existingToolCall?.timestamp ||
          new Date().toISOString(),
        needsApproval:
          updates.needsApproval ?? existingToolCall?.needsApproval ?? false,
        ...existingToolCall,
        ...updates,
      };

      newToolCalls.set(toolCallId, updatedToolCall);

      set({
        toolCallStates: { ...state.toolCallStates, [chatId]: newToolCalls },
      });

      logger.debug("Updated tool call state:", {
        chatId,
        toolCallId,
        updates,
        finalState: updatedToolCall,
      });
    }
  },

  getToolCallState: (chatId: string, toolCallId: string) => {
    const state = get();
    const toolCalls = state.toolCallStates[chatId];
    return toolCalls?.get(toolCallId);
  },

  getUnifiedToolCallData: (chatId: string, toolCallId: string) => {
    const state = get();
    const toolCalls = state.toolCallStates[chatId];
    const toolCallState = toolCalls?.get(toolCallId);

    if (!toolCallState) {
      return undefined;
    }

    return {
      toolCall: toolCallState.toolCall,
      result: toolCallState.result,
      status: toolCallState.status,
      approval: toolCallState.approval,
      needsApproval: toolCallState.needsApproval,
    };
  },

  cancelToolCall: async (_chatId: string, toolCallId: string) => {
    try {
      await api.toolCalls.cancel(toolCallId);
      logger.info("Tool call cancelled successfully", toolCallId);
    } catch (error) {
      logger.error("Failed to cancel tool call", toolCallId, error);
      throw error;
    }
  },

  convertToBackground: async (_chatId: string, toolCallId: string) => {
    try {
      const result = await api.toolCalls.convertToBackground(toolCallId);
      logger.info(
        "Tool call converted to background successfully",
        toolCallId,
        result.process_id,
      );
      return result.process_id;
    } catch (error) {
      logger.error(
        "Failed to convert tool call to background",
        toolCallId,
        error,
      );
      throw error;
    }
  },

  getExecutingToolCalls: (chatId: string) => {
    const state = get();
    const toolCalls = state.toolCallStates[chatId];
    if (!toolCalls) return [];

    return Array.from(toolCalls.values()).filter(
      (toolCall) =>
        toolCall.status === "executing" ||
        toolCall.status === "writing_input" ||
        toolCall.status === "pending",
    );
  },

  // Dismiss unread state when viewing a chat
  // This is fire-and-forget - the global WebSocket will update our state
  dismissChat: async (chatId: string) => {
    // Skip if chat is not unread (no need to dismiss). Read from the RQ cache —
    // the single source of truth for Chat objects.
    const chatObj = getChatFromCache(chatId);
    if (!chatObj?.unread) {
      return;
    }

    try {
      const result = await api.chatsV2.dismiss(chatId);

      // If state actually changed, update local state immediately for responsiveness
      // (global WebSocket will also deliver this update)
      if (result.changed) {
        // Patch the RQ caches (list + detail) — unread drives the sidebar.
        patchChatCaches(
          useProjectStore.getState().currentProject?.id,
          chatId,
          { unread: false }
        );
        logger.debug("[ChatStore] Dismissed unread for chat", {
          chatId,
        });
      }
    } catch (error) {
      // Non-blocking - just log the error
      logger.warn("[ChatStore] Failed to dismiss chat:", error);
    }
  },

  // Get computed busy state for a chat (delegates to activityStore as single source of truth)
  getIsChatBusy: (chatId: string) => {
    const activity = useActivityStore.getState().activities.get(chatId);
    return activity !== undefined && activity >= ChatActivity.RUNNING;
  },

  // Release all retained state for one chat. The messages cache and the
  // per-chat slices below have no TTL (gcTime: Infinity / plain records), so
  // deleted/archived chats must be evicted explicitly or they leak until
  // logout. Chat objects themselves (chatKeys.list/detail) are removed by
  // removeChatFromListCache in the delete path; this handles everything else.
  evictChat: (chatId: string) => {
    clearStreamingBuffersForChat(chatId);
    clearMessagesCache(chatId);
    useThreadActivityStore.getState().clearThreads(chatId);

    const omit = <T>(record: Record<string, T>): Record<string, T> => {
      if (!(chatId in record)) return record;
      const { [chatId]: _evicted, ...rest } = record;
      return rest;
    };
    set((state) => ({
      discussMode: omit(state.discussMode),
      errorEvents: omit(state.errorEvents),
      infoEvents: omit(state.infoEvents),
      runOutputs: omit(state.runOutputs),
      nodeExecutions: omit(state.nodeExecutions),
      toolCallStates: omit(state.toolCallStates),
      streamingMessages: omit(state.streamingMessages),
      finalizedStreamIds: omit(state.finalizedStreamIds),
      toolResultsByCallId: omit(state.toolResultsByCallId),
      contextUsage: omit(state.contextUsage),
    }));
  },

  // Reset store to initial state (for logout)
  reset: () => {
    // Clear all streaming buffers (safety net for any orphaned buffers)
    // Note: streamingBuffers is keyed by "chatId:thread", so we clear all
    for (const key of streamingBuffers.keys()) {
      const buffer = streamingBuffers.get(key);
      if (buffer?.flushTimeoutId) {
        clearTimeout(buffer.flushTimeoutId);
      }
    }
    streamingBuffers.clear();

    // Unsubscribe from chat details in the unified stream
    try {
      getGlobalUpdatesStore()?.unsubscribeFromChatDetails();
    } catch {
      // May fail during teardown
    }

    // Clear thread activity store
    useThreadActivityStore.getState().clearAll();

    // Clear module-scoped set that tracks processed task tool calls
    processedTaskToolCallIds.clear();

    // Drop the homed Chat objects from the React Query caches (the single
    // source of truth) so a logout clears chat data everywhere.
    queryClient.removeQueries({ queryKey: chatKeys.all });
    // Messages are homed in the RQ cache — drop all their entries too.
    clearAllMessagesCache();

    // Reset to initial state
    set({
      discussMode: {},
      errorEvents: {},
      infoEvents: {},
      runOutputs: {},
      nodeExecutions: {},
      streamingMessages: {},
      finalizedStreamIds: {},
      contextUsage: {},
      toolResultsByCallId: {},
      toolCallStates: {},
      activeChatId: null,
      hasLoaded: false,
      error: null,
      pendingStatusFetches: {},
    });
  },
}));