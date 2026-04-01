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
import { YieldStatus } from "../gen/reliant/v1/yield_pb";
import type { ProcessedMessage } from "../lib/messageProcessor";
import { processMessage } from "../lib/messageProcessor";

// Chat streaming is handled via the unified gRPC stream in globalUpdatesStore
import type {
  ChatUpdate,
  ProtoMessageUpdate,
  ToolApprovalUpdate,
  ActiveThreadUpdate,
  ToolCallUpdate,
  ErrorUpdate,
  InfoUpdate,
  SkillInvocationUpdate,
  WorkflowStatusUpdate,
  StreamingDelta,
  RunOutputUpdate,
  NodeExecutionUpdate,
  YieldUpdate,
} from "../types/streaming";

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
import { yieldGrpc, type YieldInfo } from "../api/yield-grpc";

import { useProjectStore } from "./projectStore";
import { useActivityStore, ChatActivity } from "./activityStore";
import { useThreadActivityStore } from "./threadActivityStore";
import { useChatNavigationStore } from "./chatNavigationStore";
import { useTasksStore } from "./tasksStore";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useWorktreeStore } from "./worktreeStore";
import { useChatParamsStore } from "./chatParamsStore";
import { useAttachmentStore } from "./attachmentStore";
import type { Attachment } from "../api/client";
import { logger } from "../lib/logger";
import { singleflight } from "../lib/singleflight";
import { DEFAULT_WORKFLOW } from "./preferencesStore";
import { tabSwitchProfiler } from "../lib/tabSwitchProfiler";
import {
  STREAMING_FLUSH_TIMEOUT_MS,
} from "../lib/constants";

// Lazy getter to avoid circular dependency with globalUpdatesStore.
// Uses a cached dynamic import instead of synchronous require() which
// Vite's ESM bundler cannot resolve during circular initialization.
let _globalUpdatesStoreModule: typeof import("./globalUpdatesStore") | null = null;

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

// Clear all buffers for a chat (all threads)
function clearAllStreamingBuffers(chatId: string): void {
  const prefix = `${chatId}:`;
  for (const key of streamingBuffers.keys()) {
    if (key.startsWith(prefix)) {
      const buffer = streamingBuffers.get(key);
      if (buffer?.flushTimeoutId) {
        clearTimeout(buffer.flushTimeoutId);
      }
      streamingBuffers.delete(key);
    }
  }
}

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

// Helper to trim messages array to prevent memory bloat
// NOTE: Trimming is disabled until proper "load more" UI is implemented.
// The pagination infrastructure exists (loadMoreMessages) but no UI calls it,
// so trimming would cause users to lose access to older messages.
function trimMessages(messages: Message[]): Message[] {
  return messages;
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
        allAttachments.push({
          id: att.id,
          filename: att.filename,
          size: BigInt(att.size),
          mimeType: att.mime_type,
          url: att.url,
        } as unknown as Attachment);
      }
    }
  });

  return allAttachments;
}

// Track which task-related tool call IDs have already been processed
// to prevent duplicate API calls when messages are reprocessed (cross-matching, streaming updates)
const processedTaskToolCallIds = new Set<string>();

// ============================================================================
// LRU Cache for Per-Chat Data
// ============================================================================
// Keeps the last N chats' heavy data (messages, processedMessages, etc.) in memory.
// When a user switches to chat N+1, the least-recently-used chat's data is evicted.
// Chat metadata (chats Map, chatOrder) is always retained for sidebar rendering.
// Evicted chats reload their data via subscribeToChatDetails on re-select.

const LRU_MAX_SIZE = 5;
const recentChatIds: string[] = []; // [most recent, ..., oldest]

// Evict heavy per-chat data while preserving Chat metadata and chatOrder.
// This is similar to cleanupChatState but does NOT remove from chats Map or chatOrder.
function evictChatData(chatId: string, store: { get: () => ChatStoreState; set: (partial: Partial<ChatStoreState>) => void }): void {
  const state = store.get();

  clearAllStreamingBuffers(chatId);
  useThreadActivityStore.getState().clearThreads(chatId);

  const newMessages = { ...state.messages };
  const newProcessedMessages = { ...state.processedMessages };
  const newApprovals = { ...state.approvals };
  const newPendingApprovals = { ...state.pendingApprovals };
  const newPendingYields = { ...state.pendingYields };
  const newErrorEvents = { ...state.errorEvents };
  const newInfoEvents = { ...state.infoEvents };
  const newSkillInvocations = { ...state.skillInvocations };
  const newRunOutputs = { ...state.runOutputs };
  const newNodeExecutions = { ...state.nodeExecutions };
  const newToolCallStates = { ...state.toolCallStates };
  const newStreamingMessages = { ...state.streamingMessages };
  const newContextUsage = { ...state.contextUsage };
  const newMessagePagination = { ...state.messagePagination };
  const newPendingStatusFetches = { ...state.pendingStatusFetches };

  delete newMessages[chatId];
  delete newProcessedMessages[chatId];
  delete newApprovals[chatId];
  delete newPendingApprovals[chatId];
  delete newPendingYields[chatId];
  delete newErrorEvents[chatId];
  delete newInfoEvents[chatId];
  delete newSkillInvocations[chatId];
  delete newRunOutputs[chatId];
  delete newNodeExecutions[chatId];
  delete newToolCallStates[chatId];
  delete newStreamingMessages[chatId];
  delete newContextUsage[chatId];
  delete newMessagePagination[chatId];
  delete newPendingStatusFetches[chatId];

  store.set({
    messages: newMessages,
    processedMessages: newProcessedMessages,
    approvals: newApprovals,
    pendingApprovals: newPendingApprovals,
    pendingYields: newPendingYields,
    errorEvents: newErrorEvents,
    infoEvents: newInfoEvents,
    skillInvocations: newSkillInvocations,
    runOutputs: newRunOutputs,
    nodeExecutions: newNodeExecutions,
    toolCallStates: newToolCallStates,
    streamingMessages: newStreamingMessages,
    contextUsage: newContextUsage,
    messagePagination: newMessagePagination,
    pendingStatusFetches: newPendingStatusFetches,
  });

  // Clear processed task tool call IDs — re-processing is idempotent so this is safe.
  // This prevents the module-level Set from growing unboundedly across evictions.
  processedTaskToolCallIds.clear();

  logger.info("[LRU] Evicted chat data", { chatId: chatId.slice(0, 8) });
}

// Touch a chatId in the LRU — moves it to front, evicts oldest if over capacity.
// Must be called AFTER activeChatId is set so we never evict the active chat.
function touchLRU(chatId: string, store: { get: () => ChatStoreState; set: (partial: Partial<ChatStoreState>) => void }): void {
  // Remove if already present
  const idx = recentChatIds.indexOf(chatId);
  if (idx !== -1) {
    recentChatIds.splice(idx, 1);
  }

  // Add to front (most recent)
  recentChatIds.unshift(chatId);

  // Evict oldest entries beyond LRU_MAX_SIZE
  while (recentChatIds.length > LRU_MAX_SIZE) {
    const evictId = recentChatIds.pop()!;
    // Never evict the active chat (safety check)
    if (evictId === store.get().activeChatId) {
      recentChatIds.unshift(evictId);
      continue;
    }
    evictChatData(evictId, store);
  }
}

// Remove a chatId from the LRU tracking (e.g., on chat deletion)
function removeFromLRU(chatId: string): void {
  const idx = recentChatIds.indexOf(chatId);
  if (idx !== -1) {
    recentChatIds.splice(idx, 1);
  }
}

// Clear the LRU tracking entirely (e.g., on reset/logout)
function clearLRU(): void {
  recentChatIds.length = 0;
}

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
interface ToolCallState {
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
  // Single source of truth for all chat objects — O(1) lookup by ID
  chats: Map<string, Chat>;
  // Explicit ordering for sidebar rendering (list of chat IDs)
  chatOrder: string[];
  messages: Record<string, Message[]>;
  approvals: Record<string, ToolApprovalRequest[]>;
  pendingApprovals: Record<string, ToolApprovalRequest[]>;
  pendingYields: Record<string, YieldInfo | null>;
  errorEvents: Record<string, ErrorUpdate[]>; // Error events from workflow/activity failures
  infoEvents: Record<string, InfoUpdate[]>; // Info notifications (shown to user, not saved to thread)
  skillInvocations: Record<string, SkillInvocationUpdate[]>; // Skill lifecycle timeline events
  runOutputs: Record<string, RunOutputUpdate[]>; // Run step outputs from workflow execution
  nodeExecutions: Record<string, NodeExecutionUpdate[]>; // Node execution events from workflow activities
  // NOTE: chatActivity has been REMOVED - use activityStore instead
  // Activity state comes from activityStore, populated by the server's ChatActivity enum
  toolCallStates: Record<string, Map<string, ToolCallState>>;
  streamingMessages: Record<string, Record<string, Message | null> | undefined>; // Currently streaming messages per chat+thread (temporary, replaced by complete message)

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

  // Pagination state for lazy loading messages
  messagePagination: Record<
    string,
    {
      total: number;
      hasMore: boolean;
      oldestOrdinal: number;
      isLoadingMore: boolean;
    }
  >;

  // Pre-processed message data for fast rendering (indexed by chatId -> messageId)
  // This cache stores parsed message content to avoid re-parsing on every render/tab switch
  processedMessages: Record<string, Map<string, ProcessedMessage>>;

  // Track which chat is currently active (for UI purposes)
  activeChatId: string | null;

  // Track worktree ID for new chat (when user clicks "new chat" in a specific workspace)
  pendingNewChatWorktreeId: string | null;

  // Global loading/error states
  isLoading: boolean;
  error: string | null;

  // Track chats currently being deleted (to prevent duplicate delete operations)
  deletingChatIds: Set<string>;

  // Track pending status fetches to prevent duplicate API calls
  pendingStatusFetches: Record<string, Promise<unknown>>;

  // Archived chats (loaded once, updated via gRPC stream)
  archivedChats: Chat[];
  archivedChatsLoaded: boolean;

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
  deleteChat: (id: string) => Promise<void>;
  renameChat: (id: string, newTitle: string) => Promise<void>;
  // Methods for chat state management
  initChatState: (chat: Chat) => void;
  cleanupChatState: (chatId: string) => void;

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
    },
  ) => Promise<void>;
  loadMessages: (chatId: string) => Promise<void>;
  loadMoreMessages: (chatId: string) => Promise<void>;

  // Stream methods (chat detail events are delivered via the unified global stream)
  processChatStreamUpdates: (chatId: string, updates: ChatUpdate[], isSnapshot?: boolean) => void;

  // Chat control methods
  cancelChat: (chatId: string) => Promise<void>;
  pauseChat: (chatId: string) => Promise<void>;
  resumeChat: (chatId: string) => Promise<void>;
  forceYieldThread: (chatId: string, threadId: string) => Promise<void>;
  refreshChat: (chatId: string) => Promise<void>;
  forceRecalculateBusyState: (chatId: string) => void;
  forceResetChatToIdle: (chatId: string) => void;
  checkChatStatus: (chatId: string) => Promise<void>;
  stopStreaming: (chatId: string) => void;
  dismissChat: (chatId: string) => Promise<void>;
  markUnread: (chatId: string) => Promise<void>;

  // Tool approval methods
  addPendingApproval: (chatId: string, approval: ToolApprovalRequest) => void;
  approveToolRequest: (
    chatId: string,
    requestId: string,
    actionTaken?: string,
  ) => Promise<void>;
  denyToolRequest: (
    chatId: string,
    requestId: string,
    denialReason?: string,
    actionTaken?: string,
  ) => Promise<void>;
  resolveYield: (chatId: string, yieldId: string, action: string) => Promise<void>;
  approveAllPending: (chatId: string, actionTaken?: string) => Promise<void>;
  denyAllPending: (
    chatId: string,
    denialReason?: string,
    actionTaken?: string,
  ) => Promise<void>;

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

  // Archived chats management (loaded once, updated via gRPC stream)
  loadArchivedChats: (projectId?: string) => Promise<void>;
  addArchivedChat: (chat: Chat) => void;
  removeArchivedChat: (chatId: string) => Chat | null;

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
  chats: new Map<string, Chat>(),
  chatOrder: [],
  messages: {},
  approvals: {},
  pendingApprovals: {},
  pendingYields: {},
  errorEvents: {},
  infoEvents: {},
  skillInvocations: {},
  runOutputs: {},
  nodeExecutions: {},
  toolCallStates: {},
  streamingMessages: {}, // Currently streaming messages per chat+thread
  contextUsage: {}, // Context usage tracking for compaction indicator
  processedMessages: {}, // Pre-processed message data for fast rendering
  messagePagination: {}, // Pagination state for lazy loading messages
  activeChatId: null,
  pendingNewChatWorktreeId: null,
  isLoading: false,
  error: null,
  deletingChatIds: new Set(),
  pendingStatusFetches: {},
  archivedChats: [],
  archivedChatsLoaded: false,

  // Load all chats (with singleflight deduplication to prevent parallel API calls)
  loadChats: async () => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) {
      set({ chats: new Map(), chatOrder: [], isLoading: false });
      return;
    }

    // Use singleflight to ensure only one load per project runs at a time
    // The key includes projectId to allow different projects to load simultaneously
    return singleflight(`loadChats:${projectId}`, async () => {
      set({ isLoading: true, error: null });
      try {
        const response = await api.chatsV2.list(projectId);

        // Preserve existing timestamps to prevent reordering on refresh
        // Only use backend timestamp if it's actually newer (real update)
        const currentChats = get().chats;

        // Create a map of API chats for deduplication
        const apiChatMap = new Map<string, Chat>();
        (response || []).forEach((chat: Chat) => {
          const existingTimestamp = currentChats.get(chat.id)?.updatedAt;
          // Use existing timestamp to preserve sort order
          // Backend may update timestamp on read, but we only want to update on actual changes
          const chatWithTimestamp = existingTimestamp
            ? { ...chat, updatedAt: existingTimestamp }
            : chat;
          apiChatMap.set(chat.id, chatWithTimestamp);
        });

        // Get fresh state to catch any optimistically-added chats
        // This prevents race conditions where createChat adds a chat after we captured currentChats
        const freshChats = get().chats;

        // Merge: API chats take precedence, but include any optimistically-added chats not yet in API response
        const mergedChats = new Map<string, Chat>(apiChatMap);

        // Add any optimistically-added chats that aren't in the API response yet
        for (const [id, chat] of freshChats) {
          if (!mergedChats.has(id)) {
            mergedChats.set(id, chat);
          }
        }

        // Build chatOrder from API response order, then append optimistic chats
        const chatOrder: string[] = [];
        for (const chat of apiChatMap.values()) {
          chatOrder.push(chat.id);
        }
        for (const [id] of freshChats) {
          if (!apiChatMap.has(id)) {
            chatOrder.push(id);
          }
        }

        set({
          chats: mergedChats,
          chatOrder,
          isLoading: false,
        });

        // Merge activity from ListChats into activityStore.
        // Guard: protect recent optimistic non-IDLE values from being
        // downgraded by a server response that may predate the optimistic
        // set.  Once the anti-downgrade window expires the server value
        // is authoritative — this prevents permanently-stuck RUNNING
        // states when a CHAT_ACTIVITY_CHANGED event is missed.
        const activityState = useActivityStore.getState();
        const currentActivities = activityState.activities;
        const merged = new Map(currentActivities);
        for (const chat of apiChatMap.values()) {
          const serverActivity = (chat.activity ?? 0) as ChatActivity;
          if (
            serverActivity !== ChatActivity.IDLE ||
            !activityState.isFreshNonIdle(chat.id)
          ) {
            merged.set(chat.id, serverActivity);
          }
        }
        // Remove entries for chats no longer returned by the server
        // (deleted or moved to another project)
        for (const chatId of merged.keys()) {
          if (!apiChatMap.has(chatId) && !freshChats.has(chatId)) {
            merged.delete(chatId);
          }
        }
        useActivityStore.getState().setActivities(merged);
      } catch (error) {
        logger.error("Failed to load chats:", error);
        set({ error: "Failed to load chats", isLoading: false });
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

    // Add to chats map (if not already present)
    // The chat might already be in the map if the chat_created event arrived
    // and triggered loadChats() before this code resumed after await
    set((state) => {
      if (state.chats.has(chatId)) {
        return state;
      }
      const newChats = new Map(state.chats);
      newChats.set(chatId, chat);
      return {
        chats: newChats,
        chatOrder: [...state.chatOrder, chatId],
      };
    });

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

      set((state) => ({
        messages: {
          ...state.messages,
          [chatId]: [optimisticUserMessage],
        },
      }));
    }

    // Optimistically mark as RUNNING so the thinking indicator shows immediately
    // The backend will confirm via CHAT_ACTIVITY_CHANGED event shortly
    useActivityStore.getState().setActivity(chatId, ChatActivity.RUNNING);

    return chat;
  },

  // Delete a chat (archive on first delete, permanent on second)
  deleteChat: async (id: string) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) {
      throw new Error("No project selected");
    }

    // Prevent duplicate delete operations
    const state = get();
    if (state.deletingChatIds.has(id)) {
      logger.warn(
        `Chat ${id} is already being deleted, ignoring duplicate delete request`,
      );
      return;
    }

    // Check if chat exists
    const chatToDelete = state.chats.get(id);
    if (!chatToDelete) {
      logger.warn(`Chat ${id} does not exist, ignoring delete request`);
      return;
    }

    // Save chat data before removing from active list (for archive scenario)
    const savedChatData = { ...chatToDelete };

    // Mark chat as being deleted
    const newDeletingIds = new Set(state.deletingChatIds);
    newDeletingIds.add(id);
    set({ deletingChatIds: newDeletingIds, isLoading: true, error: null });

    // Optimistically remove chat from active list immediately
    set((state) => {
      const newChats = new Map(state.chats);
      newChats.delete(id);
      return {
        chats: newChats,
        chatOrder: state.chatOrder.filter((cid) => cid !== id),
        activeChatId: state.activeChatId === id ? null : state.activeChatId,
      };
    });

    try {
      const result = await api.chatsV2.delete(id);

      // Remove from navigation queue
      const { removeFromQueue } = useChatNavigationStore.getState();
      removeFromQueue(id);

      // If archived (not permanently deleted), add to archived list
      if (!result.permanently_deleted) {
        // Add to archived chats list with state = 'archived'
        get().addArchivedChat({ ...savedChatData, state: ChatState.ARCHIVED });
        logger.info(
          `Chat ${id.slice(0, 8)} archived and added to archived list`,
        );
      } else {
        logger.info(`Chat ${id.slice(0, 8)} permanently deleted`);
      }

      // Clean up chat state
      get().cleanupChatState(id);

      // Remove from deletingChatIds
      const finalState = get();
      const finalDeletingIds = new Set(finalState.deletingChatIds);
      finalDeletingIds.delete(id);
      set({ deletingChatIds: finalDeletingIds, isLoading: false });
    } catch (error) {
      logger.error("Failed to delete chat:", error);

      // On error, restore the chat to the active list
      set((state) => {
        const newChats = new Map(state.chats);
        newChats.set(id, savedChatData);
        return {
          chats: newChats,
          chatOrder: [...state.chatOrder, id],
        };
      });

      // Remove from deletingChatIds
      const errorState = get();
      const errorDeletingIds = new Set(errorState.deletingChatIds);
      errorDeletingIds.delete(id);
      set({
        deletingChatIds: errorDeletingIds,
        error: "Failed to delete chat",
        isLoading: false,
      });
    }
  },

  // Rename a chat
  renameChat: async (id: string, newTitle: string) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) {
      throw new Error("No project selected");
    }

    // Optimistically update the title in local state
    set((state) => {
      const existing = state.chats.get(id);
      if (!existing) return state;
      const newChats = new Map(state.chats);
      newChats.set(id, { ...existing, title: newTitle });
      return { chats: newChats };
    });

    try {
      // Update the chat on the backend
      const updatedChat = await api.chatsV2.update(id, { title: newTitle });

      // Update local state with the response from backend
      set((state) => {
        const newChats = new Map(state.chats);
        newChats.set(id, updatedChat);
        return { chats: newChats };
      });

      logger.info(`Chat ${id} renamed to "${newTitle}"`);
    } catch (error) {
      logger.error("Failed to rename chat:", error);

      // On error, revert the optimistic update
      // We need to fetch the original title from the backend or keep a backup
      await get().loadChats(projectId);

      throw new Error("Failed to rename chat");
    }
  },

  // Initialize state for a chat
  initChatState: (chat: Chat) => {
    const chatId = chat.id;

    // Use callback form of set() to ensure we're working with fresh state
    // This prevents race conditions when multiple state updates happen in sequence
    set((state) => {
      // Skip if already initialized
      if (state.chats.has(chatId)) {
        return state;
      }

      // Store chat in the Map
      const newChats = new Map(state.chats);
      newChats.set(chatId, chat);

      return {
        chats: newChats,
        chatOrder: state.chatOrder.includes(chatId) ? state.chatOrder : [...state.chatOrder, chatId],
        messages: { ...state.messages, [chatId]: [] },
        approvals: { ...state.approvals, [chatId]: [] },
        pendingApprovals: { ...state.pendingApprovals, [chatId]: [] },
        pendingYields: { ...state.pendingYields, [chatId]: null },
        toolCallStates: { ...state.toolCallStates, [chatId]: new Map() },
        processedMessages: { ...state.processedMessages, [chatId]: new Map() },
        skillInvocations: { ...state.skillInvocations, [chatId]: [] },
      };
    });

    // Activity is NOT synced here. The authoritative paths are:
    // 1. loadChats (merge into activityStore on initial load / project switch)
    // 2. CHAT_ACTIVITY_CHANGED real-time events (globalUpdatesStore)
    // 3. Optimistic updates from user actions (cancel/pause/resume/create)
    // Writing activity from a potentially stale chat object would overwrite
    // correct real-time values.
  },

  // Clean up state for a chat (full removal — called on chat deletion)
  cleanupChatState: (chatId: string) => {
    const state = get();

    clearAllStreamingBuffers(chatId);

    // Clear thread activity for this chat
    useThreadActivityStore.getState().clearThreads(chatId);

    // Remove stale activity entry
    useActivityStore.getState().removeActivity(chatId);

    // Remove from LRU tracking
    removeFromLRU(chatId);

    // Remove from all normalized records (including metadata)
    const newChats = new Map(state.chats);
    newChats.delete(chatId);
    const newMessages = { ...state.messages };
    const newApprovals = { ...state.approvals };
    const newPendingApprovals = { ...state.pendingApprovals };
    const newPendingYields = { ...state.pendingYields };
    const newToolCallStates = { ...state.toolCallStates };
    const newProcessedMessages = { ...state.processedMessages };
    const newErrorEvents = { ...state.errorEvents };
    const newInfoEvents = { ...state.infoEvents };
    const newSkillInvocations = { ...state.skillInvocations };
    const newRunOutputs = { ...state.runOutputs };
    const newNodeExecutions = { ...state.nodeExecutions };
    const newStreamingMessages = { ...state.streamingMessages };
    const newContextUsage = { ...state.contextUsage };
    const newMessagePagination = { ...state.messagePagination };
    const newPendingStatusFetches = { ...state.pendingStatusFetches };

    delete newMessages[chatId];
    delete newApprovals[chatId];
    delete newPendingApprovals[chatId];
    delete newPendingYields[chatId];
    delete newToolCallStates[chatId];
    delete newProcessedMessages[chatId];
    delete newErrorEvents[chatId];
    delete newInfoEvents[chatId];
    delete newSkillInvocations[chatId];
    delete newRunOutputs[chatId];
    delete newNodeExecutions[chatId];
    delete newStreamingMessages[chatId];
    delete newContextUsage[chatId];
    delete newMessagePagination[chatId];
    delete newPendingStatusFetches[chatId];

    set({
      chats: newChats,
      chatOrder: state.chatOrder.filter((cid) => cid !== chatId),
      messages: newMessages,
      approvals: newApprovals,
      pendingApprovals: newPendingApprovals,
      pendingYields: newPendingYields,
      toolCallStates: newToolCallStates,
      processedMessages: newProcessedMessages,
      errorEvents: newErrorEvents,
      infoEvents: newInfoEvents,
      skillInvocations: newSkillInvocations,
      runOutputs: newRunOutputs,
      nodeExecutions: newNodeExecutions,
      streamingMessages: newStreamingMessages,
      contextUsage: newContextUsage,
      messagePagination: newMessagePagination,
      pendingStatusFetches: newPendingStatusFetches,
    });
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
        messageCount: get().messages[chat.id]?.length || 0,
        hasProcessedMessages: !!get().processedMessages[chat.id]?.size,
      });
    }

    const { activeChatId, chats } = get();

    // If already selected, don't reload
    if (activeChatId === chat.id) {
      return;
    }

    // Check if this is a new chat or existing one
    const isNewChat = !chats.has(chat.id);

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
      // Update chats Map with the chat info
      // Mode is tracked in chatParamsStore, not on chat object
      set((state) => {
        const newChats = new Map(state.chats);
        newChats.set(chat.id, chat);
        return { chats: newChats };
      });
    }

    // Set as active and clear pending new chat worktree
    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("set-activeChatId");
    }

    set({
      activeChatId: chat.id,
      pendingNewChatWorktreeId: null, // Clear pending worktree when selecting a chat
    });

    // Update LRU — must be after activeChatId is set so the active chat is protected
    touchLRU(chat.id, { get, set });

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

    // Reset state for new chats or chats whose data was evicted by LRU.
    // An evicted chat is an existing chat (in chats Map) but with no messages data.
    const state = get();
    const chatId = chat.id;
    const isEvicted = !isNewChat && !(chatId in state.messages);

    if (isNewChat || isEvicted) {
      // Initialize with empty state — data will be populated by subscribeToChatDetails
      if (isEvicted) {
        logger.info("[LRU] Re-selecting evicted chat, will reload via subscription", {
          chatId: chatId.slice(0, 8),
        });
      }
      set({
        messages: { ...state.messages, [chatId]: [] },
        approvals: { ...state.approvals, [chatId]: [] },
        pendingApprovals: { ...state.pendingApprovals, [chatId]: [] },
        pendingYields: { ...state.pendingYields, [chatId]: null },
      });
    } else {
      // For existing chats with cached data, preserve messages and approvals
      // Activity state is managed by activityStore (populated by server events)
      // They will be refreshed by the async loads below
      if (tabSwitchProfiler.isEnabled()) {
        tabSwitchProfiler.mark("existing-chat-reuse-state", {
          messagesCount: state.messages[chatId]?.length || 0,
          processedCount: state.processedMessages[chatId]?.size || 0,
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
      }
    })();

    // Load pending approvals from API (fire-and-forget)
    // This ensures we have fresh approval state even if WebSocket missed updates
    // The API (ListPendingApprovalsByChat) only returns pending approvals
    void (async () => {
      try {
        const pendingApprovals = await api.approvals.listByChat(
          chat.id,
        );
        const state = get();
        set({
          pendingApprovals: {
            ...state.pendingApprovals,
            [chat.id]: pendingApprovals,
          },
        });
        logger.debug("[ChatStore] Loaded pending approvals", {
          chatId: chat.id.slice(0, 8),
          count: pendingApprovals.length,
        });
      } catch (error) {
        logger.error("[ChatStore] Failed to load approvals:", error);
      }
    })();

    // Load pending yield from API (fire-and-forget)
    void (async () => {
      try {
        const pendingYield = await yieldGrpc.getPendingYield(chat.id);
        const state = get();
        set({
          pendingYields: {
            ...state.pendingYields,
            [chat.id]: pendingYield,
          },
        });
        if (pendingYield) {
          logger.debug("[ChatStore] Loaded pending yield", {
            chatId: chat.id.slice(0, 8),
            yieldId: pendingYield.yield_id.slice(0, 8),
          });
        }
      } catch (error) {
        logger.error("[ChatStore] Failed to load pending yield:", error);
      }
    })();

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
      set({
        activeChatId: null,
        pendingNewChatWorktreeId: worktreeId || null,
      });

      // Stop polling per-chat events for the deselected chat
      getGlobalUpdatesStore().unsubscribeFromChatDetails();

      // Clear from workspace state
      const projectId = useProjectStore.getState().currentProject?.id;
      if (projectId) {
        useWorkspaceStateStore
          .getState()
          .setActiveChatId(projectId, targetWorktreeId, null);
      }
    } else {
      // Just set the pending worktree if there's no active chat
      set({ pendingNewChatWorktreeId: worktreeId || null });
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
    },
  ) => {
    const state = get();
    if (!state.chats.has(chatId)) {
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

      set((state) => {
        // Update chat timestamp when message is sent so it moves to top of list
        const existing = state.chats.get(chatId);
        const newChats = existing
          ? new Map(state.chats).set(chatId, { ...existing, updatedAt: new Date().toISOString() })
          : state.chats;
        return {
          chats: newChats,
          // Add optimistic user message to ensure correct layer grouping
          messages: {
            ...state.messages,
            [chatId]: [...(state.messages[chatId] || []), optimisticUserMessage],
          },
        };
      });

      // Use V2 message endpoint (no project ID required)
      // Model is now configured via workflow params or presets, not global preferences
      const workflowParams = options?.workflowParams || {};

      // If there's a pending yield, include its ID so the backend resolves it
      const pendingYield = get().pendingYields[chatId];

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
        ...(pendingYield && { yield_id: pendingYield.yield_id }),
      };

      const response = await api.chatsV2.sendMessage(
        chatId,
        content,
        attachmentIds,
        Object.keys(sendOptions).length > 0 ? sendOptions : undefined,
      );

      logger.log("Message sent successfully:", response);

      // Note: Optimistic user message will be replaced when the real message arrives via WebSocket
      // The hasRealUserMessage check in the WebSocket handler handles this automatically

      // Optimistically update chat workflow_name if workflow changed
      // Note: Agent is now a workflow param, not stored on chat - no optimistic update needed
      if (options?.workflow !== undefined && options.workflow) {
        set((state) => {
          const currentChat = state.chats.get(chatId);
          if (!currentChat) return state;

          const newChats = new Map(state.chats);
          newChats.set(chatId, { ...currentChat, workflow_name: options.workflow });
          return { chats: newChats };
        });
      }

      // Update workflow metadata from response so Temporal links stay current
      if (response.workflowId || response.runId) {
        set((state) => {
          const currentChat = state.chats.get(chatId);
          if (!currentChat) return state;

          const newChats = new Map(state.chats);
          newChats.set(chatId, {
            ...currentChat,
            ...(response.workflowId
              ? { workflowId: response.workflowId }
              : {}),
            ...(response.runId
              ? { runId: response.runId }
              : {}),
          } as Chat);

          return { chats: newChats };
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
      const messages = response.messages || [];

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
      const trimmedMessages = trimMessages(messages);

      // PERFORMANCE: Pre-process all messages for fast rendering on tab switches
      // This is critical - without this, each ChatMessage parses JSON on every render
      // which causes 800ms+ delays when switching to chats with many messages
      const approvals = state.approvals[chatId] || [];
      const existingProcessed = state.processedMessages[chatId] || new Map();
      const newProcessedMessages = new Map(existingProcessed);

      // Process each message that isn't already processed
      // Use processMessage directly (matching WebSocket handler)
      for (const message of trimmedMessages) {
        if (!newProcessedMessages.has(message.id)) {
          const processed = processMessage(message, approvals);
          newProcessedMessages.set(message.id, processed);
        }
      }

      logger.info(
        `[ChatStore] Pre-processed ${newProcessedMessages.size} messages for fast rendering`,
        {
          chatId: chatId.slice(0, 8),
          loadedCount: trimmedMessages.length,
          processedCount: newProcessedMessages.size,
        },
      );

      set({
        messages: { ...state.messages, [chatId]: trimmedMessages },
        processedMessages: {
          ...state.processedMessages,
          [chatId]: newProcessedMessages,
        },
        messagePagination: {
          ...state.messagePagination,
          [chatId]: {
            total: response.total,
            hasMore: response.hasMore,
            oldestOrdinal: response.oldestOrdinal,
            isLoadingMore: false,
          },
        },
      });
    } catch (error) {
      logger.error("[ChatStore] Failed to load messages:", error);
    }
  },

  // Load more (older) messages for a chat using pagination
  loadMoreMessages: async (chatId: string) => {
    const state = get();
    const pagination = state.messagePagination?.[chatId];

    logger.log("[ChatStore] loadMoreMessages called:", {
      chatId: chatId.slice(0, 8),
      pagination,
    });

    // Don't load if no pagination info, no more messages, or already loading
    if (!pagination?.hasMore || pagination.isLoadingMore) {
      logger.log("[ChatStore] loadMoreMessages skipped:", {
        hasMore: pagination?.hasMore,
        isLoadingMore: pagination?.isLoadingMore,
      });
      return;
    }

    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) return;

    // Mark as loading
    set({
      messagePagination: {
        ...state.messagePagination,
        [chatId]: {
          ...pagination,
          isLoadingMore: true,
        },
      },
    });

    try {
      // Load 100 more messages before the oldest we have via gRPC
      const response = await api.chatsV2.listMessages(chatId, {
        recent: 100,
        beforeOrdinal: pagination.oldestOrdinal,
      });
      const olderMessages = response.messages || [];

      // Prepend older messages to existing messages
      const currentState = get();
      const existingMessages = currentState.messages[chatId] || [];

      // PERFORMANCE: Pre-process older messages for fast rendering
      const approvals = currentState.approvals[chatId] || [];
      const existingProcessed =
        currentState.processedMessages[chatId] || new Map();
      const newProcessedMessages = new Map(existingProcessed);

      for (const message of olderMessages) {
        if (!newProcessedMessages.has(message.id)) {
          const processed = processMessage(message, approvals);
          newProcessedMessages.set(message.id, processed);
        }
      }

      set({
        messages: {
          ...currentState.messages,
          [chatId]: [...olderMessages, ...existingMessages],
        },
        processedMessages: {
          ...currentState.processedMessages,
          [chatId]: newProcessedMessages,
        },
        messagePagination: {
          ...currentState.messagePagination,
          [chatId]: {
            total: response.total,
            hasMore: response.hasMore,
            oldestOrdinal: response.oldestOrdinal,
            isLoadingMore: false,
          },
        },
      });

      logger.info("[ChatStore] Loaded more messages:", {
        chatId: chatId.slice(0, 8),
        loaded: olderMessages.length,
        total: existingMessages.length + olderMessages.length,
        processedCount: newProcessedMessages.size,
        hasMore: response.hasMore,
      });
    } catch (error) {
      logger.error("[ChatStore] Failed to load more messages:", error);

      // Reset loading state on error
      const currentState = get();
      set({
        messagePagination: {
          ...currentState.messagePagination,
          [chatId]: {
            ...currentState.messagePagination[chatId],
            isLoadingMore: false,
          },
        },
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

      if (currentMsg && currentMsg.contentBlocks) {
        contentBlocks = [...currentMsg.contentBlocks] as Array<ContentBlock & { status?: string }>;
      }

      for (const delta of deltas) {
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

      const normalizedThread = normalizeThreadKey(chatId, thread);
      const streamingId =
        currentMsg?.id || `streaming-temp-${normalizedThread}`;

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
        const skillInvocationUpdates = updates.filter(
          (u): u is SkillInvocationUpdate => u.update_type === "skill_invocation",
        );
        const runOutputUpdates = updates.filter(
          (u): u is RunOutputUpdate => u.update_type === "run_output",
        );
        const nodeExecutionUpdates = updates.filter(
          (u): u is NodeExecutionUpdate => u.update_type === "node_execution",
        );
        const yieldUpdates = updates.filter(
          (u): u is YieldUpdate => u.update_type === "yield",
        );
        if (yieldUpdates.length > 0) {
          logger.info("[ChatStore] Stream received yield updates", {
            chatId: chatId.slice(0, 8),
            count: yieldUpdates.length,
            yields: yieldUpdates.map(y => ({ id: y.yield_id?.slice(0, 8), status: y.status })),
          });
        }

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

            // Process buffered deltas asynchronously (they were delayed by the buffer timeout)
            const threadKey = normalizeThreadKey(chatId, thread);
            const chatStreaming = get().streamingMessages[chatId] || {};
            let currentStreamingMsg = chatStreaming[threadKey] || null;

            // If the existing streaming message is already finished (cancelled),
            // clear it before processing new deltas - don't build on stale content
            if (currentStreamingMsg?.streamingState === StreamingState.COMPLETE) {
              logger.debug(
                "[Streaming] Clearing finished streaming message before buffered flush",
                {
                  chatId: chatId.slice(0, 8),
                  thread: threadKey.slice(0, 8),
                  oldMsgId: currentStreamingMsg.id,
                },
              );
              currentStreamingMsg = null;
            }

            const flushedMsg = processStreamingDeltas(
              bufferedDeltas,
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
          rawStreamingDeltas,
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
          skillInvocationUpdates.length > 0 ||
          runOutputUpdates.length > 0 ||
          nodeExecutionUpdates.length > 0 ||
          toolCallUpdates.length > 0 ||
          allToolExecutionUpdates.length > 0;

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
            let currentStreamingMsg = chatStreaming[threadKey] || null;

            // If the existing streaming message is already finished (cancelled),
            // clear it before processing new deltas - don't build on stale content
            if (currentStreamingMsg?.streamingState === StreamingState.COMPLETE) {
              logger.debug(
                "[Streaming] Clearing finished streaming message before new stream",
                {
                  chatId: chatId.slice(0, 8),
                  thread: threadKey.slice(0, 8),
                  oldMsgId: currentStreamingMsg.id,
                },
              );
              currentStreamingMsg = null;
            }

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
        // First pass: collect all tool_results from tool messages in this update batch
        // messageUpdates are now proto Message objects (camelCase fields)
        const batchToolResultsMap = new Map<
          string,
          { content: string; is_error?: boolean; tool_name?: string }
        >();
        messageUpdates.forEach((protoMsg) => {
          if (protoMsg.role === MessageRole.TOOL) {
            protoMsg.contentBlocks.forEach((block) => {
              if (block.type === ContentBlockType.TOOL_RESULT && block.toolCallId) {
                batchToolResultsMap.set(block.toolCallId, {
                  content: block.content || "",
                  is_error: block.isError,
                  tool_name: block.toolName,
                });
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
            contentBlocks: (protoMsg.contentBlocks || []).map((b: any) => ({
              ...b,
              type: typeof b.type === 'string' ? b.type : b.type,
            })),
          };
          return converted;
        };

        // Helper to embed matchedResult in tool_call blocks
        const embedMatchedResults = (
          msg: Message,
        ): Message => {
          if (msg.role !== MessageRole.ASSISTANT) {
            return msg;
          }
          const enhancedBlocks = (msg.contentBlocks || []).map((block) => {
            if (block.type === ContentBlockType.TOOL_CALL && block.toolCallId) {
              const matchedResult = batchToolResultsMap.get(block.toolCallId);
              if (matchedResult) {
                return {
                  ...block,
                  matchedResult: {
                    toolCallId: block.toolCallId,
                    type: "tool_result",
                    name: matchedResult.tool_name || block.toolName || "",
                    content: matchedResult.content,
                    isError: matchedResult.is_error || false,
                  },
                };
              }
            }
            return block;
          });
          return {
            ...msg,
            contentBlocks: enhancedBlocks,
          } as Message;
        };

        // Convert proto messages and embed matched results
        const messages: Message[] = messageUpdates.map((msg) =>
          embedMatchedResults(convertProtoMsg(msg)),
        );

        // Track which threads have complete messages BEFORE the set() call
        // This is needed for clearing streaming buffers after state update
        const completedThreadsForBufferClear = new Set<string>();
        messages.forEach((m) => {
          if (m.role === MessageRole.ASSISTANT && isProtoMessageComplete(m)) {
            const threadKey = normalizeThreadKey(chatId, m.thread);
            completedThreadsForBufferClear.add(threadKey);
          }
        });

        set((state) => {
          // Initialize chats Map if this chat doesn't exist (can happen if snapshot arrives before initChatState)
          let chatsMap = state.chats;
          if (!chatsMap.has(chatId)) {
            logger.warn(
              "[ChatStore] Received updates for uninitialized chat, initializing now",
              {
                chatId: chatId.slice(0, 8),
                messageCount: messages.length,
              },
            );
            // Create minimal chat data to allow messages to be stored
            // The full chat data will be loaded separately
            const minimalChat: Chat = {
              id: chatId,
              userId: "",
              title: "",
              projectId: useProjectStore.getState().currentProject?.id || "",
              state: ChatState.IDLE,
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
              lastActive: new Date().toISOString(),
            } as Chat;
            chatsMap = new Map(chatsMap);
            chatsMap.set(chatId, minimalChat);
          }

          // CRITICAL FIX: Read fresh state to avoid race condition
          // Multiple rapid WebSocket updates can cause stale state reads
          // Using get() ensures we see the latest messages, not the state captured
          // when this set() callback was queued
          const freshState = get();

          // Check if we received a complete assistant message (replaces streaming)
          const hasCompleteAssistantMessage = messages.some(
            (m) => m.role === MessageRole.ASSISTANT && isProtoMessageComplete(m),
          );

          // SNAPSHOT vs INCREMENTAL message handling:
          // Snapshots REPLACE all messages to prevent cross-chat contamination.
          // Incremental updates MERGE with existing messages.
          let updatedMessages: Message[];

          if (isSnapshot) {
            // Snapshot: replace entirely with the snapshot's messages
            logger.info("[ChatStore] Snapshot: replacing messages for chat", {
              chatId: chatId.slice(0, 8),
              snapshotCount: messages.length,
              previousCount: (freshState.messages[chatId] || []).length,
            });
            updatedMessages = [...messages];
          } else {
            // Incremental: merge with existing messages
            const existingMessages = freshState.messages[chatId] || [];
            updatedMessages = [...existingMessages];

            // Check if we received a real user message (replaces optimistic)
            const hasRealUserMessage = messages.some(
              (m) => m.role === MessageRole.USER && !m.id.startsWith("optimistic-user-"),
            );

            // Remove optimistic user message if a real user message arrived
            if (hasRealUserMessage) {
              updatedMessages = updatedMessages.filter(
                (m) => !m.id.startsWith("optimistic-user-"),
              );
            }

            messages.forEach((newMessage) => {
              const existingIndex = updatedMessages.findIndex(
                (m) => m.id === newMessage.id,
              );

              if (existingIndex >= 0) {
                // Update existing message
                updatedMessages[existingIndex] = newMessage;
              } else {
                // Add new message
                updatedMessages.push(newMessage);
              }
            });
          }

          // Cross-match tool results with existing assistant messages
          // This handles the case where tool results arrive after the assistant message
          // Track which messages were modified so we can reprocess them for the processedMessages cache
          const crossMatchedMessageIds = new Set<string>();
          if (batchToolResultsMap.size > 0) {
            updatedMessages = updatedMessages.map((msg) => {
              if (msg.role !== MessageRole.ASSISTANT) return msg;

              const blocks = msg.contentBlocks || [];
              let modified = false;

              const updatedBlocks = blocks.map((block) => {
                if (
                  block.type === ContentBlockType.TOOL_CALL &&
                  block.toolCallId &&
                  !block.matchedResult
                ) {
                  const matchedResult = batchToolResultsMap.get(block.toolCallId);
                  if (matchedResult) {
                    modified = true;
                    return {
                      ...block,
                      matchedResult: {
                        toolCallId: block.toolCallId,
                        type: "tool_result",
                        name: matchedResult.tool_name || block.toolName || "",
                        content: matchedResult.content,
                        isError: matchedResult.is_error || false,
                      },
                    };
                  }
                }
                return block;
              });

              if (modified) {
                crossMatchedMessageIds.add(msg.id);
                return {
                  ...msg,
                  contentBlocks: updatedBlocks,
                };
              }
              return msg;
            });
          }

          // Gather all streaming messages: newly processed ones merged with existing ones
          // Thread-aware: each thread can have its own streaming message
          // CRITICAL: Use freshState to avoid race condition with stale state
          const existingChatStreaming =
            freshState.streamingMessages[chatId] || {};
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

          // Track which threads have complete assistant messages
          const completedThreads = new Set<string>();
          messages.forEach((m) => {
            if (m.role === MessageRole.ASSISTANT && isProtoMessageComplete(m)) {
              const threadKey = normalizeThreadKey(chatId, m.thread);
              completedThreads.add(threadKey);
            }
          });

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
              // Find streaming-temp message for this thread
              const streamingMsg = mergedStreamingMsgs.get(threadKey);
              if (
                streamingMsg &&
                streamingMsg.id.startsWith("streaming-temp")
              ) {
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
                      !msg.id.startsWith("streaming-temp") &&
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

            // Remove streaming-temp messages from updatedMessages for completed threads
            updatedMessages = updatedMessages.filter((m) => {
              if (!m.id.startsWith("streaming-temp")) return true;
              const msgThreadKey = normalizeThreadKey(chatId, m.thread);
              return !completedThreads.has(msgThreadKey);
            });
          }

          // Add/update streaming messages for threads that don't have complete messages
          for (const [threadKey, streamingMsg] of mergedStreamingMsgs) {
            // Skip if this thread has a complete message
            if (completedThreads.has(threadKey)) continue;

            const streamingMsgExists = updatedMessages.some(
              (m) =>
                m.id === streamingMsg.id &&
                normalizeThreadKey(chatId, m.thread) === threadKey,
            );
            if (!streamingMsgExists) {
              logger.debug(
                "[Streaming] Adding streaming message to message list",
                {
                  chatId: chatId.slice(0, 8),
                  thread: threadKey.slice(0, 8),
                  streamingMsgId: streamingMsg.id,
                  messageCount: updatedMessages.length,
                },
              );
              updatedMessages.push(streamingMsg);
            } else {
              logger.debug(
                "[Streaming] Streaming message already exists in list, updating in place",
                {
                  chatId: chatId.slice(0, 8),
                  thread: threadKey.slice(0, 8),
                  streamingMsgId: streamingMsg.id,
                },
              );
              // Update the existing streaming message in place
              const existingIndex = updatedMessages.findIndex(
                (m) =>
                  m.id === streamingMsg.id &&
                  normalizeThreadKey(chatId, m.thread) === threadKey,
              );
              if (existingIndex >= 0) {
                updatedMessages[existingIndex] = streamingMsg;
              }
            }
          }

          // Sort by createdAt timestamp in descending order (newest first) for bottom-up rendering
          // IMPORTANT: Using createdAt instead of ordinal because ordinal is per-thread, not global.
          updatedMessages.sort((a, b) => {
            const timeA = new Date(a.createdAt || "").getTime();
            const timeB = new Date(b.createdAt || "").getTime();
            return timeB - timeA; // Descending (newest first)
          });

          // Process approval updates (both traditional and workflow-based)
          // CRITICAL: Use freshState to avoid race condition
          const updatedApprovals = [...(freshState.approvals[chatId] || [])];
          let updatedPendingApprovals = [
            ...(freshState.pendingApprovals[chatId] || []),
          ];

          allApprovals.forEach((approvalUpdate) => {
            // Convert ToolApprovalUpdate to ToolApprovalRequest format
            const approval: ToolApprovalRequest = {
              id: approvalUpdate.id,
              chat_id: approvalUpdate.chat_id,
              content_block_id:
                approvalUpdate.content_block_id || approvalUpdate.entity_id,
              status: toApprovalStatus(approvalUpdate.status),
              denial_reason: approvalUpdate.denial_reason,
              created_at: approvalUpdate.created_at,
              responded_at: approvalUpdate.responded_at,
              actions: approvalUpdate.actions, // Configured action buttons from workflow
              action_taken: approvalUpdate.action_taken, // Which action was clicked
            };

            // Update or add approval
            const existingIndex = updatedApprovals.findIndex(
              (a) => a.id === approval.id,
            );
            if (existingIndex >= 0) {
              updatedApprovals[existingIndex] = approval;
            } else {
              updatedApprovals.push(approval);
            }

            // Update pending approvals list
            if (approval.status === ApprovalStatus.PENDING) {
              if (!updatedPendingApprovals.some((a) => a.id === approval.id)) {
                updatedPendingApprovals.push(approval);
              }
            } else {
              // Remove from pending if no longer pending
              updatedPendingApprovals = updatedPendingApprovals.filter(
                (a) => a.id !== approval.id,
              );
            }
          });

          // Process thread updates -> write to threadActivityStore (not chatStore)
          if (threadUpdates.length > 0) {
            const threadActivityState = useThreadActivityStore.getState();
            const existingThreads = threadActivityState.threads[chatId] || [];
            const updatedThreads = [...existingThreads];
            threadUpdates.forEach((threadUpdate) => {
              const existingIndex = updatedThreads.findIndex(
                (t) => t.id === threadUpdate.id,
              );
              if (existingIndex >= 0) {
                const existing = updatedThreads[existingIndex];
                updatedThreads[existingIndex] = {
                  ...existing,
                  ...threadUpdate,
                  // Preserve thread identity fields when completion updates omit them
                  thread_title: threadUpdate.thread_title || existing.thread_title,
                  spawned_by_node_id: threadUpdate.spawned_by_node_id || existing.spawned_by_node_id,
                };
              } else {
                updatedThreads.push(threadUpdate);
              }
            });
            threadActivityState.setThreads(chatId, updatedThreads);
          }

          // Note: activity updates come through the global stream and are
          // handled by activityStore, so we don't need to fetch status separately
          // when threads complete.

          // Process error updates
          // Each error persists in the chat - no deduplication.
          // Errors are sorted by timestamp in the timeline so they appear
          // in chronological order alongside messages.
          // CRITICAL: Use freshState to avoid race condition
          // On snapshot: start fresh to avoid stale/duplicate events from previous subscription
          const updatedErrorEvents = isSnapshot
            ? []
            : [...(freshState.errorEvents[chatId] || [])];
          errorUpdates.forEach((errorUpdate) => {
            // Only dedupe by exact ID to prevent duplicates from re-polling
            const existingIndex = updatedErrorEvents.findIndex(
              (e) => e.id === errorUpdate.id,
            );
            if (existingIndex >= 0) {
              // Update existing error (same ID means same error event)
              updatedErrorEvents[existingIndex] = errorUpdate;
            } else {
              // New error - add it to the list
              updatedErrorEvents.push(errorUpdate);
            }
          });

          // Process info updates (notifications shown to user, not saved to thread)
          // CRITICAL: Use freshState to avoid race condition
          // On snapshot: start fresh to avoid stale/duplicate events
          const updatedInfoEvents = isSnapshot
            ? []
            : [...(freshState.infoEvents[chatId] || [])];
          infoUpdates.forEach((infoUpdate) => {
            // Only dedupe by exact ID to prevent duplicates from re-polling
            const existingIndex = updatedInfoEvents.findIndex(
              (e) => e.id === infoUpdate.id,
            );
            if (existingIndex >= 0) {
              // Keep the original timestamp for stable timeline placement.
              // Skill notices can be re-emitted across multiple LLM passes in a turn,
              // and replacing timestamp would cause the same notice to "jump" downward.
              const existing = updatedInfoEvents[existingIndex];
              updatedInfoEvents[existingIndex] = {
                ...infoUpdate,
                timestamp: existing.timestamp || infoUpdate.timestamp,
              };
            } else {
              // New info - add it to the list
              updatedInfoEvents.push(infoUpdate);
            }
          });

          const updatedSkillInvocations = isSnapshot
            ? []
            : [...(freshState.skillInvocations[chatId] || [])];
          skillInvocationUpdates.forEach((skillInvocationUpdate) => {
            const existingIndex = updatedSkillInvocations.findIndex(
              (e) => e.id === skillInvocationUpdate.id,
            );
            if (existingIndex >= 0) {
              const existing = updatedSkillInvocations[existingIndex];
              updatedSkillInvocations[existingIndex] = {
                ...skillInvocationUpdate,
                timestamp: existing.timestamp || skillInvocationUpdate.timestamp,
              };
            } else {
              updatedSkillInvocations.push(skillInvocationUpdate);
            }
          });

          // Process run output updates (workflow run step outputs)
          // CRITICAL: Use freshState to avoid race condition
          // On snapshot: start fresh to avoid stale/duplicate events
          const updatedRunOutputs = isSnapshot
            ? []
            : [...(freshState.runOutputs[chatId] || [])];
          runOutputUpdates.forEach((runOutput) => {
            // Add or update run output (dedup by unique_activity_id)
            const existingIndex = updatedRunOutputs.findIndex(
              (r) =>
                r.unique_activity_id &&
                r.unique_activity_id === runOutput.unique_activity_id,
            );
            if (existingIndex >= 0) {
              updatedRunOutputs[existingIndex] = runOutput;
            } else {
              updatedRunOutputs.push(runOutput);
            }
          });
          // Sort by sequence_number for consistent ordering
          updatedRunOutputs.sort(
            (a, b) => (a.sequence_number || 0) - (b.sequence_number || 0),
          );

          // Process node execution updates (workflow activity lifecycle events)
          // CRITICAL: Use freshState to avoid race condition
          // On snapshot: start fresh to avoid stale/duplicate events
          const updatedNodeExecutions = isSnapshot
            ? []
            : [...(freshState.nodeExecutions[chatId] || [])];

          nodeExecutionUpdates.forEach((nodeExec) => {
            // Add or update node execution (dedup by node_id + event_type)
            const existingIndex = updatedNodeExecutions.findIndex(
              (n) =>
                n.node_id === nodeExec.node_id &&
                n.event_type === nodeExec.event_type,
            );
            if (existingIndex >= 0) {
              updatedNodeExecutions[existingIndex] = nodeExec;
            } else {
              updatedNodeExecutions.push(nodeExec);
            }
          });
          // Sort by sequence_number for consistent ordering
          updatedNodeExecutions.sort(
            (a, b) => (a.sequence_number || 0) - (b.sequence_number || 0),
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

          // Process tool execution state updates
          // CRITICAL: Use fresh state to avoid race condition with rapid updates
          const existingToolCallStates =
            freshState.toolCallStates[chatId] || new Map();
          const updatedToolCallStates = new Map(existingToolCallStates);

          // Terminal statuses that should not be overwritten
          const terminalStatuses = new Set(["cancelled", "backgrounded"]);

          allToolExecutionUpdates.forEach((update) => {
            const existing = updatedToolCallStates.get(update.tool_call_id);
            const mappedStatus: ToolCallState["status"] =
              update.status === "denied" ? "failed" : update.status;

            // Don't allow "completed" to overwrite terminal statuses like "cancelled" or "backgrounded"
            // This prevents race conditions where the tool finishes right after user clicks cancel/background
            if (
              existing &&
              terminalStatuses.has(existing.status) &&
              mappedStatus === "completed"
            ) {
              return; // Skip this update
            }

            const toolCallState: ToolCallState = {
              ...existing,
              id: update.tool_call_id,
              sessionId: chatId,
              toolName: update.tool_name,
              status: mappedStatus,
              timestamp: update.timestamp,
            };

            updatedToolCallStates.set(update.tool_call_id, toolCallState);
          });

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

          // Process messages for fast rendering
          // This pre-parses message content so components can render without re-parsing
          // On snapshot: start fresh to avoid stale processed messages
          // On incremental: merge with existing processed messages
          const existingProcessed = isSnapshot
            ? new Map()
            : (freshState.processedMessages[chatId] || new Map());
          const newProcessedMessages = new Map(existingProcessed);

          // Only process new/updated messages (not all messages)
          // This is critical for performance - we only process what changed
          const messagesToProcess = [
            ...messages, // New/updated messages from this WebSocket update
          ];

          // Also reprocess streaming messages if present (all threads)
          for (const streamingMsg of processedStreamingMsgs.values()) {
            messagesToProcess.push(streamingMsg);
          }

          // CRITICAL: Also reprocess assistant messages that were cross-matched with tool results
          // When tool results arrive, we embed them in the assistant message's tool_call blocks.
          // Without reprocessing, the processedMessages cache has stale data and the UI
          // won't show the tool output until a re-render (like switching chats).
          if (crossMatchedMessageIds.size > 0) {
            const crossMatchedMessages = updatedMessages.filter((msg) =>
              crossMatchedMessageIds.has(msg.id),
            );
            logger.debug(
              "[Streaming] Reprocessing cross-matched assistant messages",
              {
                chatId: chatId.slice(0, 8),
                messageIds: Array.from(crossMatchedMessageIds).map((id) =>
                  id.slice(0, 8),
                ),
                toolResultCount: batchToolResultsMap.size,
              },
            );
            messagesToProcess.push(...crossMatchedMessages);
          }

          messagesToProcess.forEach((message) => {
            const processed = processMessage(message, updatedApprovals);
            newProcessedMessages.set(message.id, processed);

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

          // Context usage comes from onContextUsage callback, not from message updates
          const updatedContextUsage = { ...(state.contextUsage[chatId] || {}) };

          // Process yield updates
          let updatedPendingYield = state.pendingYields[chatId] ?? null;
          if (yieldUpdates.length > 0) {
            for (const yieldUpdate of yieldUpdates) {
              if (yieldUpdate.status === "pending") {
                updatedPendingYield = {
                  yield_id: yieldUpdate.yield_id,
                  chat_id: yieldUpdate.chat_id,
                  workflow_id: yieldUpdate.workflow_id,
                  step_id: yieldUpdate.step_id,
                  status: YieldStatus.PENDING,
                  created_at: new Date().toISOString(),
                };
              } else if (yieldUpdate.status === "resolved") {
                updatedPendingYield = null;
              }
            }
          }

          const newState = {
            messages: { ...state.messages, [chatId]: updatedMessages },
            processedMessages: {
              ...state.processedMessages,
              [chatId]: newProcessedMessages,
            },
            approvals: { ...state.approvals, [chatId]: updatedApprovals },
            pendingApprovals: {
              ...state.pendingApprovals,
              [chatId]: updatedPendingApprovals,
            },
            pendingYields: yieldUpdates.length > 0
              ? { ...state.pendingYields, [chatId]: updatedPendingYield }
              : state.pendingYields,
            errorEvents: { ...state.errorEvents, [chatId]: updatedErrorEvents },
            infoEvents: { ...state.infoEvents, [chatId]: updatedInfoEvents },
            skillInvocations: {
              ...state.skillInvocations,
              [chatId]: updatedSkillInvocations,
            },
            runOutputs: { ...state.runOutputs, [chatId]: updatedRunOutputs },
            nodeExecutions: {
              ...state.nodeExecutions,
              [chatId]: updatedNodeExecutions,
            },
            toolCallStates: {
              ...state.toolCallStates,
              [chatId]: updatedToolCallStates,
            },
            // Include chats Map if it was modified (uninitialized chat case)
            ...(chatsMap !== state.chats ? { chats: chatsMap, chatOrder: state.chatOrder.includes(chatId) ? state.chatOrder : [...state.chatOrder, chatId] } : {}),
            // NOTE: chat activity is NOT updated here.
            // It is handled by handleChatActivityChanged() in globalUpdatesStore.ts
            // via the global user update stream, which populates activityStore.
            // Update context usage for compaction indicator (per-thread)
            contextUsage:
              Object.keys(updatedContextUsage).length > 0
                ? { ...state.contextUsage, [chatId]: updatedContextUsage }
                : state.contextUsage,
            // Update streaming messages state (thread-aware):
            // - For completed threads: remove their streaming messages
            // - For active threads: update with newly processed streaming messages
            // Only update if there are actual changes (avoid creating new object references unnecessarily)
            streamingMessages: (() => {
              const hasStreamingChanges =
                processedStreamingMsgs.size > 0 || completedThreads.size > 0;
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
          };

          return newState;
        });


        // CRITICAL FIX: Clear streaming buffers when complete messages arrive
        // This prevents buffer flush timeouts from firing after complete messages
        // and recreating partial streaming messages that overwrite complete ones
        // Thread-aware: only clear buffers for threads that received complete messages
        if (completedThreadsForBufferClear.size > 0) {
          for (const threadKey of completedThreadsForBufferClear) {
            clearStreamingBuffer(chatId, threadKey);
          }
          logger.debug(
            "[Streaming] Cleared streaming buffers for completed threads",
            {
              chatId: chatId.slice(0, 8),
              threads: [...completedThreadsForBufferClear].map((t) =>
                t.slice(0, 8),
              ),
            },
          );
        }
  },


  // Cancel chat - cancels all sessions in the chat
  cancelChat: async (chatId: string) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) {
      logger.warn("No project ID available for cancellation");
      return;
    }

    const state = get();
    if (!state.chats.has(chatId)) {
      logger.warn("No chat state found for chatId:", chatId);
      return;
    }

    // IMMEDIATELY update UI state for instant feedback (optimistic update)
    // NOTE: We do NOT clear thread activity here. The useIsThreadActive hook already
    // returns false when the chat is not RUNNING, so threads appear inactive.
    set((state) => {
      const existing = state.chats.get(chatId);
      if (!existing) return state;
      const newChats = new Map(state.chats);
      newChats.set(chatId, { ...existing, state: ChatState.IDLE });
      return {
        chats: newChats,
        // Mark any incomplete messages as finished
        messages: {
          ...state.messages,
          [chatId]: (state.messages[chatId] || []).map((msg) =>
            !isProtoMessageComplete(msg)
              ? {
                  ...msg,
                  streamingState: StreamingState.COMPLETE,
                }
              : msg,
          ),
        },
      };
    });

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

  // Force-yield a thread - sends signal to stop a sub-thread and return to parent
  forceYieldThread: async (chatId: string, threadId: string) => {
    try {
      const { chatGrpc } = await import("../api/chat-grpc");
      await chatGrpc.forceYieldThread(chatId, threadId);
    } catch (error) {
      logger.error("Failed to force-yield thread:", error instanceof Error ? error.message : error);
    }
  },

  // Pause chat - pauses a running workflow
  pauseChat: async (chatId: string) => {
    const state = get();
    if (!state.chats.has(chatId)) {
      logger.warn("No chat state found for chatId:", chatId);
      return;
    }

    // IMMEDIATELY update UI state for instant feedback (optimistic update)
    // NOTE: We intentionally do NOT clear thread activity here. The useIsThreadActive
    // hook already returns false when the chat is not RUNNING, so threads
    // appear inactive regardless. On resume, the backend re-emits thread updates
    // to repopulate thread activity (handles multi-window and page refresh cases).
    set((state) => {
      const existing = state.chats.get(chatId);
      if (!existing) return state;
      const newChats = new Map(state.chats);
      newChats.set(chatId, { ...existing, state: ChatState.IDLE });
      return {
        chats: newChats,
        messages: {
          ...state.messages,
          [chatId]: (state.messages[chatId] || []).map((msg) =>
            !isProtoMessageComplete(msg)
              ? {
                  ...msg,
                  streamingState: StreamingState.COMPLETE,
                }
              : msg,
          ),
        },
      };
    });

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

  // Resume chat - resumes a paused or expired workflow
  // For paused: backend sends SignalResume to the running Temporal workflow
  // For expired: backend uses ResetWorkflowExecution to restore it
  // The backend ResumeChat handler detects the state and handles both transparently
  resumeChat: async (chatId: string) => {
    const state = get();
    if (!state.chats.has(chatId)) {
      logger.warn("No chat state found for chatId:", chatId);
      return;
    }

    // Optimistic update
    set((state) => {
      const existing = state.chats.get(chatId);
      if (!existing) return state;
      const newChats = new Map(state.chats);
      newChats.set(chatId, { ...existing } as Chat);
      return { chats: newChats };
    });

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

  // Refresh chat - reconnects the unified stream to re-fetch data
  refreshChat: async (chatId: string) => {
    // Data refreshes via the unified global stream reconnection
    // Re-subscribing to the chat triggers a fresh sync snapshot
    getGlobalUpdatesStore()?.subscribeToChatDetails(chatId);
  },

  // Force recalculate activity state - NO-OP since activity is now computed on-demand
  // Kept for API compatibility
  forceRecalculateBusyState: () => {},

  // Force reset a stuck chat to idle state
  // This is a recovery function for chats that are stuck in busy state
  // It clears all running state without making API calls
  forceResetChatToIdle: (chatId: string) => {
    logger.warn("⚠️ Force resetting chat to idle state (recovery mode):", {
      chatId: chatId.slice(0, 8),
    });

    set((state) => {
      const existing = state.chats.get(chatId);
      const newChats = existing
        ? new Map(state.chats).set(chatId, { ...existing, state: ChatState.IDLE } as Chat)
        : state.chats;
      // Clear thread activity in the dedicated store
      useThreadActivityStore.getState().clearThreads(chatId);

      return {
        chats: newChats,
        pendingApprovals: { ...state.pendingApprovals, [chatId]: [] },
        pendingYields: { ...state.pendingYields, [chatId]: null },
        // Mark any incomplete messages as finished
        messages: {
          ...state.messages,
          [chatId]: (state.messages[chatId] || []).map((msg) =>
            !isProtoMessageComplete(msg)
              ? {
                  ...msg,
                  streamingState: StreamingState.COMPLETE,
                }
              : msg,
          ),
        },
      };
    });

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
      // Use the get endpoint to check if chat exists and get its state
      const chat = await api.chatsV2.get(chatId);

      // NOTE: auto_approve and is_planning_mode were removed from backend
      // Mode is tracked in chatParamsStore, not on chat object
      set((s) => {
        const newChats = new Map(s.chats);
        newChats.set(chatId, chat);
        return { chats: newChats };
      });

      // Sync activity from fetched chat.  Protect recent optimistic
      // non-IDLE values from a server response that may predate them,
      // but allow the server to correct stale RUNNING states once the
      // anti-downgrade window has elapsed.
      const serverActivity = (chat.activity ?? 0) as ChatActivity;
      if (
        serverActivity !== ChatActivity.IDLE ||
        !useActivityStore.getState().isFreshNonIdle(chatId)
      ) {
        useActivityStore.getState().setActivity(chatId, serverActivity);
      }
    } catch (error) {
      logger.error("Failed to check chat status:", error);
    }
  },

  // Stop streaming - NO-OP since activity is managed by activityStore
  stopStreaming: (chatId: string) => {
    logger.debug(
      "[stopStreaming] Called (no-op - activity computed on-demand)",
      {
        chatId: chatId.slice(0, 8),
      },
    );
  },

  // Resolve a pending yield (e.g., user clicks Continue)
  resolveYield: async (chatId: string, yieldId: string, action: string) => {
    try {
      await yieldGrpc.resolveYield(yieldId, action);
      // Optimistically clear pending yield (stream update will also clear it)
      const state = get();
      set({
        pendingYields: { ...state.pendingYields, [chatId]: null },
      });
    } catch (error) {
      logger.error("[ChatStore] Failed to resolve yield:", error);
      throw error;
    }
  },

  // Add pending approval
  addPendingApproval: (chatId: string, approval: ToolApprovalRequest) => {
    const state = get();
    const currentApprovals = state.approvals[chatId] || [];
    const currentPendingApprovals = state.pendingApprovals[chatId] || [];

    const alreadyExists = currentApprovals.some((a) => a.id === approval.id);
    const newApprovals = alreadyExists
      ? currentApprovals.map((a) => (a.id === approval.id ? approval : a))
      : [...currentApprovals, approval];

    const alreadyPending = currentPendingApprovals.some(
      (a) => a.id === approval.id,
    );
    const newPending =
      approval.status === ApprovalStatus.PENDING && !alreadyPending
        ? [...currentPendingApprovals, approval]
        : currentPendingApprovals.filter((a) => a.status === ApprovalStatus.PENDING);

    // Log for debugging
    logger.info("[ChatStore] Adding pending approval:", {
      chatId,
      approvalId: approval.id,
      toolCallId: approval.tool_call_id,
      status: approval.status,
      newPendingCount: newPending.length,
    });

    set({
      approvals: { ...state.approvals, [chatId]: newApprovals },
      pendingApprovals: { ...state.pendingApprovals, [chatId]: newPending },
    });

    // Show notification if chat is not active
    if (get().activeChatId !== chatId) {
      // Notification handling removed - no longer using tabStore
    }
  },

  // Approve tool request
  approveToolRequest: async (
    chatId: string,
    requestId: string,
    actionTaken?: string,
  ) => {
    const state = get();
    const currentApprovals = state.approvals[chatId] || [];
    const currentPendingApprovals = state.pendingApprovals[chatId] || [];

    try {
      await api.approvals.approve(requestId, actionTaken);

      const updatedApprovals = currentApprovals.map((a) =>
        a.id === requestId
          ? {
              ...a,
              status: ApprovalStatus.APPROVED,
              responded_at: new Date().toISOString(),
              action_taken: actionTaken,
            }
          : a,
      );

      const s = get();
      set({
        approvals: { ...s.approvals, [chatId]: updatedApprovals },
        pendingApprovals: {
          ...s.pendingApprovals,
          [chatId]: currentPendingApprovals.filter((a) => a.id !== requestId),
        },
      });
    } catch (error) {
      logger.error("Failed to approve tool request:", error);
      throw error;
    }
  },

  // Deny tool request
  denyToolRequest: async (
    chatId: string,
    requestId: string,
    denialReason?: string,
    actionTaken?: string,
  ) => {
    const state = get();
    const currentApprovals = state.approvals[chatId] || [];
    const currentPendingApprovals = state.pendingApprovals[chatId] || [];

    try {
      await api.approvals.deny(requestId, denialReason, actionTaken);

      const updatedApprovals = currentApprovals.map((a) =>
        a.id === requestId
          ? {
              ...a,
              status: ApprovalStatus.DENIED,
              responded_at: new Date().toISOString(),
              denial_reason: denialReason || "User denied this tool call",
              action_taken: actionTaken,
            }
          : a,
      );

      const s = get();
      set({
        approvals: { ...s.approvals, [chatId]: updatedApprovals },
        pendingApprovals: {
          ...s.pendingApprovals,
          [chatId]: currentPendingApprovals.filter((a) => a.id !== requestId),
        },
      });
    } catch (error) {
      logger.error("Failed to deny tool request:", error);
      throw error;
    }
  },

  // Approve all pending
  approveAllPending: async (chatId: string, actionTaken?: string) => {
    const state = get();
    const currentApprovals = state.approvals[chatId] || [];
    const currentPendingApprovals = state.pendingApprovals[chatId] || [];
    if (currentPendingApprovals.length === 0) return;

    try {
      const requestIds = currentPendingApprovals.map((a) => a.id);
      await api.approvals.batchApprove(
        requestIds,
        actionTaken,
      );

      const s = get();
      set({
        approvals: {
          ...s.approvals,
          [chatId]: currentApprovals.map((a) =>
            requestIds.includes(a.id)
              ? {
                  ...a,
                  status: ApprovalStatus.APPROVED,
                  responded_at: new Date().toISOString(),
                  action_taken: actionTaken,
                }
              : a,
          ),
        },
        pendingApprovals: { ...s.pendingApprovals, [chatId]: [] },
      });
    } catch (error) {
      logger.error("Failed to approve all pending:", error);
    }
  },

  // Deny all pending
  denyAllPending: async (
    chatId: string,
    denialReason?: string,
    actionTaken?: string,
  ) => {
    const state = get();
    const currentApprovals = state.approvals[chatId] || [];
    const currentPendingApprovals = state.pendingApprovals[chatId] || [];
    if (currentPendingApprovals.length === 0) return;

    try {
      const requestIds = currentPendingApprovals.map((a) => a.id);
      await api.approvals.batchDeny(
        requestIds,
        denialReason,
        actionTaken,
      );

      const s = get();
      set({
        approvals: {
          ...s.approvals,
          [chatId]: currentApprovals.map((a) =>
            requestIds.includes(a.id)
              ? {
                  ...a,
                  status: ApprovalStatus.DENIED,
                  responded_at: new Date().toISOString(),
                  denial_reason:
                    denialReason || "User denied one or more tool calls",
                  action_taken: actionTaken,
                }
              : a,
          ),
        },
        pendingApprovals: { ...s.pendingApprovals, [chatId]: [] },
      });
    } catch (error) {
      logger.error("Failed to deny all pending:", error);
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

      // Add to global chat map (if not already present)
      set((state) => {
        if (state.chats.has(newChat.id)) {
          return state;
        }
        const newChats = new Map(state.chats);
        newChats.set(newChat.id, newChat);
        return {
          chats: newChats,
          chatOrder: [...state.chatOrder, newChat.id],
        };
      });

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

      // Add to global chat map (if not already present)
      set((state) => {
        if (state.chats.has(newChat.id)) {
          return state;
        }
        const newChats = new Map(state.chats);
        newChats.set(newChat.id, newChat);
        return {
          chats: newChats,
          chatOrder: [...state.chatOrder, newChat.id],
        };
      });

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

  // Archived chats management - loaded once on mount, updated via gRPC stream
  // (with singleflight deduplication to prevent parallel API calls)
  loadArchivedChats: async (projectId?: string) => {
    const resolvedProjectId =
      projectId || useProjectStore.getState().currentProject?.id;
    if (!resolvedProjectId) {
      return;
    }

    // Skip if already loaded (subsequent updates come through gRPC stream)
    if (get().archivedChatsLoaded) {
      return;
    }

    // Use singleflight to ensure only one load per project runs at a time
    return singleflight(`loadArchivedChats:${resolvedProjectId}`, async () => {
      // Double-check inside singleflight in case another call completed while we waited
      if (get().archivedChatsLoaded) {
        return;
      }

      try {
        const allArchived = await api.chatsV2.listArchived();
        // Filter by current project
        const projectChats = allArchived.filter(
          (c) => c.projectId === resolvedProjectId,
        );
        set({
          archivedChats: projectChats,
          archivedChatsLoaded: true,
        });
      } catch (error) {
        logger.error("Failed to load archived chats:", error);
      }
    });
  },

  addArchivedChat: (chat: Chat) => {
    const { archivedChats } = get();
    // Check if chat already exists to avoid duplicates
    if (
      archivedChats.some(
        (c) => c.id === chat.id,
      )
    ) {
      return;
    }
    set({ archivedChats: [...archivedChats, chat] });
  },

  removeArchivedChat: (chatId: string) => {
    const { archivedChats } = get();
    const chat = archivedChats.find(
      (c) => c.id === chatId,
    );
    if (!chat) return null;
    set({
      archivedChats: archivedChats.filter(
        (c) => c.id !== chatId,
      ),
    });
    return chat;
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
    // Skip if chat is not unread (no need to dismiss)
    const chatObj = get().chats.get(chatId);
    if (!chatObj?.unread) {
      return;
    }

    try {
      const result = await api.chatsV2.dismiss(chatId);

      // If state actually changed, update local state immediately for responsiveness
      // (global WebSocket will also deliver this update)
      if (result.changed) {
        set((state) => {
          const existing = state.chats.get(chatId);
          if (!existing) return state;
          const newChats = new Map(state.chats);
          newChats.set(chatId, { ...existing, unread: false });
          return { chats: newChats };
        });
        logger.debug("[ChatStore] Dismissed unread for chat", {
          chatId,
        });
      }
    } catch (error) {
      // Non-blocking - just log the error
      logger.warn("[ChatStore] Failed to dismiss chat:", error);
    }
  },

  // Mark chat as unread
  // This is fire-and-forget - the global WebSocket will update our state
  markUnread: async (chatId: string) => {
    try {
      const result = await api.chatsV2.markUnread(chatId);

      // If state actually changed, update local state immediately for responsiveness
      // (global WebSocket will also deliver this update)
      if (result.changed) {
        set((state) => {
          const existing = state.chats.get(chatId);
          if (!existing) return state;
          const newChats = new Map(state.chats);
          newChats.set(chatId, { ...existing, unread: true });
          return { chats: newChats };
        });
        logger.debug("[ChatStore] Marked chat as unread", { chatId });
      }
    } catch (error) {
      // Non-blocking - just log the error
      logger.warn("[ChatStore] Failed to mark chat as unread:", error);
    }
  },

  // Get computed busy state for a chat (delegates to activityStore as single source of truth)
  getIsChatBusy: (chatId: string) => {
    const activity = useActivityStore.getState().activities.get(chatId);
    return activity !== undefined && activity >= ChatActivity.RUNNING;
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
      getGlobalUpdatesStore().unsubscribeFromChatDetails();
    } catch {
      // May fail during teardown
    }

    // Clear thread activity store
    useThreadActivityStore.getState().clearAll();

    // Clear module-scoped set that tracks processed task tool calls
    processedTaskToolCallIds.clear();

    // Clear LRU tracking
    clearLRU();

    // Reset to initial state
    set({
      chats: new Map<string, Chat>(),
      chatOrder: [],
      messages: {},
      approvals: {},
      pendingApprovals: {},
      pendingYields: {},
      errorEvents: {},
      infoEvents: {},
      skillInvocations: {},
      runOutputs: {},
      nodeExecutions: {},
      streamingMessages: {},
      contextUsage: {},
      processedMessages: {},
      messagePagination: {},
      toolCallStates: {},
      activeChatId: null,
      pendingNewChatWorktreeId: null,
      isLoading: false,
      error: null,
      deletingChatIds: new Set(),
      pendingStatusFetches: {},
      archivedChats: [],
      archivedChatsLoaded: false,
    });
  },
}));
