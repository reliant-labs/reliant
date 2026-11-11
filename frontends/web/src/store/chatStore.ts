import { create } from "zustand";
import type { Chat, Message } from "../api/client";
import type { ContentBlock } from "../types/chat";
import {
  MessageRole,
  ContentBlockType,
  StreamingState,
  ChatState,
  AgentMessageKind,
} from "../gen/reliant/v1/chat_pb";
import type { Approval } from "../gen/reliant/v1/approval_pb";
import type { ToolResultsByCallId } from "../lib/messageProcessor";
import {
  foldToolResultMessage,
  getProcessedMessage,
} from "../lib/messageProcessor";
import { sortMessagesForDisplay } from "../lib/messageOrder";
import { registerToolResultSink } from "../lib/toolResultSink";
import {
  CHAT_MARKER_KINDS,
  extractChatMarker,
  stripChatMarker,
} from "../lib/chatMarkers";
import {
  mergeActiveThreads,
  snapshotReplacesMessages,
} from "../lib/chatStreamReducers";
import {
  createChatState,
  mergeToolResults,
  reduceChat,
  type ChatState as SdkChatState,
  type Logger as SdkLogger,
} from "@reliantlabs/sdk/client";

import { reportError } from "@/lib/errorReporting";

// Chat streaming is handled by the per-chat stream globalUpdatesStore owns
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
  // True when the client DEDUCED this status rather than being told it — the
  // only producer is the stream-abort pass, which cancels tools that had not
  // reported an outcome when their stream ended. See ToolCallState.inferred.
  inferred?: boolean;
  // For a spawn call, the child workflow (= thread) it started. See
  // ToolCallState.childWorkflowId.
  child_workflow_id?: string;
}
import { triggerRefetch, type RefetchType } from "../store/refetchStore";

import { useProjectStore } from "./projectStore";
import { useActivityStore, ChatActivity, isBusyActivity } from "./activityStore";
import { useThreadActivityStore } from "./threadActivityStore";
import { useChatNavigationStore } from "./chatNavigationStore";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useWorktreeStore } from "./worktreeStore";
import { useChatParamsStore } from "./chatParamsStore";
import { useAttachmentStore } from "./attachmentStore";
import { useModalStore } from "./modalStore";
import type { Attachment } from "../api/client";
import { logger } from "../lib/logger";
import { singleflight } from "../lib/singleflight";
import { queryClient } from "../lib/query-client";
import { notifyManager } from "@tanstack/react-query";
import { initEventBus } from "../lib/events";
import {
  beginPendingSend,
  beginQueuedSend,
  endPendingSend,
  endQueuedSend,
  failedSendId,
  attemptIdFor,
  endAttempt,
  newClientMessageId,
  optimisticSendId,
  type QueuedRow,
} from "../lib/pendingSends";
import { isDaemonConnectingError } from "../lib/daemon-errors";
import { sendWithDaemonWait } from "../lib/daemon-retry";
import { sendOnExistingChat } from "../lib/chatSendRouting";
import { applyApprovalUpdate, approvalsFromCache } from "../hooks/approval-queries";
import { approvalServiceKeys } from "../hooks/approval-service-hooks_gen";
import { applyQuestionUpdate, resolvePendingQuestion } from "../hooks/question-queries";
import { invalidatePlanTasks } from "../hooks/task-queries";
import {
  chatKeys,
  seedChatDetail,
  upsertChatInListCache,
  patchChatCaches,
  getChatFromCache,
} from "../hooks/chat-queries";
import {
  setMessagesInCache,
  setMessagesMetaInCache,
  patchMessagesCache,
  prependMessagesCache,
  getMessagesFromCache,
  getMessagesMetaFromCache,
  hasMessagesCache,
  clearMessagesCache,
  clearAllMessagesCache,
  fanOutMessagesToThreadCaches,
} from "../hooks/message-queries";
import { workflowExecutionsOfChat } from "../hooks/useWorkflowExecutions";
import { chatServiceKeys } from "../hooks/chat-service-hooks_gen";
import { DEFAULT_WORKFLOW } from "../hooks/settings-queries";
import { tabSwitchProfiler } from "../lib/tabSwitchProfiler";
import { armChatOpen, beginChatOpen, leaveChatOpen, type ChatOpenEntry } from "../lib/chatOpenInteraction";
import {
  STREAMING_FLUSH_FALLBACK_MS,
} from "../lib/constants";
import { trackEvent } from "../lib/analytics";
import * as chatActions from "../hooks/chat-actions";

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

// ============================================================================
// Streaming Delta Buffer - coalesce content deltas onto one animation frame
//
// Every commit is a Zustand set plus a React Query cache write, which
// re-renders the whole timeline and makes Virtuoso re-measure every row. At
// delta rate that is the scroll jitter users see, so content deltas are
// accumulated and committed at most once per frame — coalescing on TIME.
//
// This used to flush on newlines, which tied the repaint rate to the shape of
// the prose: markdown lists and code blocks committed many times a second,
// while a long unbroken line waited out the full fallback delay. A newline is
// a property of the text, not a signal about when to repaint.
//
// Non-content deltas (block starts, tool_use_start, cancellations) stay
// immediate — they are rare, and callers read them straight out of the
// returned array to drive tool state. Any buffered content is drained ahead of
// them so a thread's deltas stay strictly ordered.
// ============================================================================

interface StreamingBuffer {
  deltas: StreamingDelta[];
  /** Handle of the armed animation frame, or null when none is armed. */
  frameId: number | null;
  /** Watchdog for when the frame never fires (background tab, no rAF). */
  fallbackTimeoutId: ReturnType<typeof setTimeout> | null;
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

// Single-fire guard: a burst of error events (one per workflow retry attempt
// before the workflow gives up) opens exactly one modal, not N. Mirrors the
// upgradeInterceptor pattern in api/upgradeInterceptor.ts. Reset when the modal
// closes so a later quota hit in a different chat still surfaces a modal.
let _reliantManagedQuotaModalInFlight = false;

function openReliantManagedQuotaModal(errorMessage: string): void {
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
    // This path has NO headers to read: the error reaches us as a
    // Temporal-serialized marker string in the chat-error stream, so unlike
    // the Connect path (api/upgradeInterceptor.ts, which forwards the server's
    // reason verbatim) the reason has to be supplied here.
    //
    // It used to claim `free_tier_global_budget` — the service-wide operator
    // cap — on the grounds of matching the enforcement path's copy. That was
    // wrong in the common case and actively misleading: the marker only ever
    // fires for the reliant-managed driver, and by far the usual cause is the
    // caller's own credit balance hitting zero, which is what control-plane's
    // preflightBillingGate rejects. Telling that user a shared budget was
    // exhausted pointed them at someone else's problem.
    //
    // The server's own message still renders verbatim beneath the title, so if
    // the genuine cause was the global cap the user sees that text too.
    reason: "reliant_credit_exhausted",
    message: cleanMessage,
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
        openReliantManagedQuotaModal(errorUpdate.error_message);
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
    case CHAT_MARKER_KINDS.ProviderStreamStalled: {
      // The prefix already names the model and provider and says what the
      // silence means; whether the turn is retrying or paused is the row's
      // own is_retrying state. The error_summary the timeline prefers is the
      // same sentence without the marker, and the payload duplicates the
      // provider name, so there is nothing extra to surface here.
      errorUpdate.error_message = stripChatMarker(errorUpdate.error_message);
      return;
    }
    case CHAT_MARKER_KINDS.ProviderUsageLimit: {
      // The prefix names the provider, the window and when it resets, so the
      // stripped message is complete. The error_summary the timeline prefers
      // carries the same sentence without the marker.
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
      frameId: null,
      fallbackTimeoutId: null,
    };
    streamingBuffers.set(key, buffer);
  }
  return buffer;
}

// rAF is absent in non-browser environments (node test runners, SSR). Reading
// it through globalThis on every call rather than caching a reference keeps a
// test that stubs rAF working, and keeps us honest about jsdom, which does
// provide one.
function scheduleFrame(callback: () => void): number | null {
  const raf = (globalThis as { requestAnimationFrame?: (cb: () => void) => number })
    .requestAnimationFrame;
  return typeof raf === "function" ? raf(callback) : null;
}

function cancelFrame(frameId: number): void {
  const caf = (globalThis as { cancelAnimationFrame?: (id: number) => void })
    .cancelAnimationFrame;
  if (typeof caf === "function") caf(frameId);
}

// Disarm both schedulers. Every teardown path funnels through here: a frame or
// timer that survives its buffer would resurrect a placeholder for a stream
// that already ended.
function cancelBufferFlush(buffer: StreamingBuffer): void {
  if (buffer.frameId !== null) {
    cancelFrame(buffer.frameId);
    buffer.frameId = null;
  }
  if (buffer.fallbackTimeoutId !== null) {
    clearTimeout(buffer.fallbackTimeoutId);
    buffer.fallbackTimeoutId = null;
  }
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
  if (buffer) {
    cancelBufferFlush(buffer);
  }
  streamingBuffers.delete(key);
}

// Clear ALL streaming buffers for a chat (every thread). Used when the
// chat's activity goes idle and any leftover buffer is stale by definition.
function clearStreamingBuffersForChat(chatId: string): void {
  for (const key of [...streamingBuffers.keys()]) {
    if (key.startsWith(`${chatId}:`)) {
      const buffer = streamingBuffers.get(key);
      if (buffer) {
        cancelBufferFlush(buffer);
      }
      streamingBuffers.delete(key);
    }
  }
}

// ============================================================================
// The fold: @reliantlabs/sdk's reduceChat
//
// processChatStreamUpdates hands a batch to the SDK's pure reducer, which owns
// the stream protocol's rules (snapshot replaces, replay merges; deltas build
// a placeholder under the pre-allocated message id until the persisted message
// or a finalize marker retires it; a delta naming a finalized id is a stale
// tail; a stopped stream settles its unfinished tools; results join their calls
// first-write-wins; the event logs dedupe). The store is the app's holding of
// the state it returns: the transcript lives in the React Query message cache,
// the rest in the slices below.
// ============================================================================

const EMPTY_TOOL_RESULTS: ToolResultsByCallId = {};
const EMPTY_STREAMING: Readonly<Record<string, Message>> = {};
const EMPTY_FINALIZED: ReadonlySet<string> = new Set();

/** The reducer's diagnostics, on the app's logger. */
const reducerLog: SdkLogger = {
  debug: (message, meta) => logger.debug(`[ChatStore] ${message}`, meta),
  info: (message, meta) => logger.info(`[ChatStore] ${message}`, meta),
  warn: (message, meta) => logger.warn(`[ChatStore] ${message}`, meta),
  error: (message, meta) => logger.error(`[ChatStore] ${message}`, meta),
};

const REDUCE_ENV = { now: () => new Date().toISOString(), log: reducerLog };

/**
 * The chat's stream state, assembled from the store's slices and the message
 * cache. A slice the chat has none of is a shared empty value, so a batch that
 * does not touch it hands back the very same one and the commit can see that.
 */
function chatStateOf(chatId: string): SdkChatState {
  const s = useChatStore.getState();
  return {
    ...createChatState(chatId, { syncPending: false }),
    messages: getMessagesFromCache(chatId) as unknown as SdkChatState["messages"],
    toolResults: s.toolResultsByCallId[chatId] ?? EMPTY_TOOL_RESULTS,
    toolCalls: s.toolCallStates[chatId] ?? new Map(),
    streaming: (s.streamingMessages[chatId] as Record<string, Message> | undefined) ?? EMPTY_STREAMING,
    finalizedStreamIds: s.finalizedStreamIds[chatId] ?? EMPTY_FINALIZED,
    errors: s.errorEvents[chatId] ?? [],
    info: s.infoEvents[chatId] ?? [],
    runOutputs: s.runOutputs[chatId] ?? [],
    nodeExecutions: s.nodeExecutions[chatId] ?? [],
  };
}

/** An assistant message id whose stream has ended: a delta stamped with it is a stale tail. */
function isFinalizedStreamId(chatId: string, id: string | undefined): boolean {
  if (!id) return false;
  if (useChatStore.getState().finalizedStreamIds[chatId]?.has(id)) return true;
  return getMessagesFromCache(chatId).some(
    (m) => m.id === id && m.role === MessageRole.ASSISTANT && isProtoMessageComplete(m),
  );
}

/** A streamed message as the app holds it: string enums kept, attachments normalized. */
function convertProtoMessage(protoMsg: ProtoMessageUpdate["message"]): Message {
  return {
    ...protoMsg,
    attachments: normalizeMessageAttachments(protoMsg.attachments),
    contentBlocks: (protoMsg.contentBlocks || []).map((b) => ({ ...b, type: b.type })),
  } as Message;
}

/**
 * Commit a coalesced batch of content deltas for one thread. bufferStreamingDeltas
 * already deferred this onto an animation frame (or the background-tab watchdog),
 * so the whole batch is exactly one state commit and this runs synchronously: a
 * second rAF here would cost a frame of latency and never run at all on the
 * watchdog path in a backgrounded tab.
 */
function flushBufferedDeltas(chatId: string, deltas: StreamingDelta[], thread: string | undefined): void {
  // A stale flush: the buffer was cleared (a complete message arrived).
  if (!hasStreamingBuffer(chatId, thread)) {
    logger.debug("[Streaming] Skipping stale buffered flush - buffer was cleared", {
      chatId: chatId.slice(0, 8),
      thread: thread?.slice(0, 8),
    });
    return;
  }
  // Finalization can land between buffering and flush: a marker (or a persisted
  // message) that arrived in the interim retires the buffered tail.
  const flushable = deltas.filter((d) => !isFinalizedStreamId(chatId, d.message_id));
  if (flushable.length === 0) {
    logger.debug("[Streaming] Skipping buffered flush - deltas finalized before flush", {
      chatId: chatId.slice(0, 8),
      thread: thread?.slice(0, 8),
    });
    return;
  }
  const before = chatStateOf(chatId);
  const after = reduceChat(before, { kind: "updates", updates: flushable, cursor: before.cursor }, REDUCE_ENV);
  if (after.streaming === before.streaming) return;
  useChatStore.setState((s) => ({
    streamingMessages: { ...s.streamingMessages, [chatId]: after.streaming as Record<string, Message> },
  }));
}

// Coalesce streaming content deltas onto one animation frame per thread.
// Returns the deltas the caller must process immediately.
// Thread-aware: groups deltas by thread and buffers each separately, so two
// threads streaming at once never share a buffer or a commit.
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

    // Commit whatever has accumulated for this thread. Re-reads the buffer
    // from the map rather than closing over it: a chat switch, finalization,
    // or completed message between scheduling and firing DELETES the buffer,
    // and that deletion is precisely the signal to drop this flush.
    const flushBuffer = () => {
      const buf = streamingBuffers.get(bufferKey);
      if (!buf) return;
      buf.frameId = null;
      if (buf.fallbackTimeoutId !== null) {
        clearTimeout(buf.fallbackTimeoutId);
        buf.fallbackTimeoutId = null;
      }
      if (buf.deltas.length === 0) return;
      const pending = buf.deltas;
      buf.deltas = [];
      flushCallback(pending, thread);
    };

    for (const delta of threadDeltas) {
      // Skip thinking deltas entirely - not displayed, causes memory issues
      if (
        delta.delta_type === "thinking_block_start" ||
        delta.delta_type === "thinking_block_delta"
      ) {
        continue;
      }

      // Non-content deltas (tool_use_start, etc.) are handled immediately.
      // Anything already buffered goes with them, or the thread's deltas
      // would reach the store out of order.
      if (delta.delta_type !== "content_block_delta") {
        if (buffer.deltas.length > 0) {
          immediateDeltas.push(...buffer.deltas);
          buffer.deltas = [];
        }
        cancelBufferFlush(buffer);
        immediateDeltas.push(delta);
        continue;
      }

      buffer.deltas.push(delta);

      // The first delta of a frame arms the schedulers; every later one just
      // accumulates, which is what collapses a burst into a single commit.
      if (buffer.frameId === null && buffer.fallbackTimeoutId === null) {
        buffer.frameId = scheduleFrame(flushBuffer);
        // The watchdog runs ALONGSIDE the frame rather than instead of it: a
        // tab backgrounded mid-stream throttles or suspends rAF without
        // cancelling the request, so the frame may only be reached minutes
        // later. Whichever fires first commits and disarms the other. When
        // there is no rAF at all (non-browser), the watchdog is the only
        // scheduler and the behavior degrades to the old timer path.
        buffer.fallbackTimeoutId = setTimeout(
          flushBuffer,
          STREAMING_FLUSH_FALLBACK_MS,
        );
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

// How many older messages loadOlderMessages fetches per scroll-back page.
// Smaller than the 200-message initial snapshot bound: paging happens while the
// user is actively scrolling, so a smaller page keeps each round-trip cheap.
const OLDER_MESSAGES_PAGE_SIZE = 100;

// Chats with an older-messages page in flight: the synchronous concurrency
// guard. Its reactive mirror is the store's olderLoadingByChat, which the UI
// reads for the loading indicator; the two are always written together.
const olderMessagesInFlight = new Set<string>();

// Per-chat stream resume cursor: the chat update sequence this store's cached
// state for the chat is current as of, recorded when the stream leaves the
// chat. Reopening the chat resumes the stream from it (an incremental replay)
// instead of a full snapshot.
//
// The cursor is only as good as the state it describes, so it lives beside
// that state and dies with it: evictChat, reset and forceResetChatToIdle all
// drop it, and resumeChatStreamFrom refuses one whose message cache is gone.
// Module-scoped for the same reason as olderMessagesInFlight: nothing renders
// it.
const chatStreamCursors = new Map<string, bigint>();

// The pause RPC still in flight per chat. ESC fires pauseChat without
// awaiting it, so a message typed right after would otherwise race the pause
// to the server: if the send is routed first it wakes a "running" run, the
// pause then lands, and the message sits unread until the user sends again.
// sendMessage waits on this so a send never overtakes the pause the user
// issued before it. The server serializes the two as well (runs.Service
// LockRunControl); this keeps the order the user acted in from depending on
// which request the server happens to receive first.
const pausesInFlight = new Map<string, Promise<void>>();


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
    | "backgrounded" // Detached; its real outcome streams in later
    | "completed" // Successfully completed
    | "denied" // Denied approval
    | "failed"; // Execution failed
  timestamp: string;
  // True when the client DEDUCED this status rather than being told it. A
  // stream ending under a tool that never reported an outcome is evidence the
  // tool stopped, but only evidence: the tool may have been past the point of
  // no return and gone on to finish, and its result then arrives normally.
  // An inferred cancel therefore yields to a real outcome, while a cancel the
  // server reported outranks a completion racing in behind it.
  inferred?: boolean;
  // For a spawn call, the workflow it started — and, since a spawned
  // sub-agent's thread id equals its workflow id, the thread its card
  // previews. Delivered live on the spawn's tool_call status event, because
  // the persisted tool-call block predates the spawn and never carries it.
  // A durable fact: once known it is never cleared by a later status.
  childWorkflowId?: string;
  error?: string;
  // Unified approval state
  approval?: Approval;
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

  // Chats with an older-messages page in flight (scroll-back paging). The
  // store owns this so every caller sees the real fetch: a duplicate call that
  // returns early at the in-flight guard never touches it.
  olderLoadingByChat: Record<string, boolean>;

  // Track which chat is currently active (for UI purposes)
  activeChatId: string | null;

  // Global loading/error states
  hasLoaded: boolean;
  error: string | null;

  // Track pending status fetches to prevent duplicate API calls
  pendingStatusFetches: Record<string, Promise<unknown>>;

  // Methods for chat management
  loadChats: () => Promise<void>;
  startChat: (
    worktreeId?: string,
    firstMessage?: string,
    attachmentIds?: string[],
    workflowParams?: Record<string, unknown>,
    workflow?: string | null,
    selectedPresets?: Record<string, string>,
    // Where it runs: a machine (daemonId), no machine by design (noMachine),
    // or neither for default resolution. See lib/chatMachine.ts.
    machine?: { daemonId?: string; noMachine?: boolean },
  ) => Promise<Chat>;
  // First send of an existing PENDING chat (e.g. a branch): StartChat with chatId.
  startExistingChat: (
    chatId: string,
    firstMessage: string,
    attachmentIds?: string[],
    options?: {
      workflow?: string | null;
      workflowParams?: Record<string, unknown>;
      selectedPresets?: Record<string, string>;
      // Same as sendMessage's: the id this message keeps on screen.
      clientMessageId?: string;
    },
  ) => Promise<Chat>;
  // Methods for chat state management
  initChatState: (chat: Chat) => void;

  // Methods for active chat
  setActiveChat: (chatId: string | null) => void;
  // deferPersist skips recording the chat as the worktree's active chat: a
  // caller still resolving the chat's worktree context calls persistActiveChat
  // once that context is current (see selectChatWithWorktreeContext).
  selectChat: (chat: Chat, options?: { deferPersist?: boolean; openEntry?: ChatOpenEntry }) => void;
  persistActiveChat: (chatId: string) => void;
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
      // The id this message keeps on screen. A caller that may send the same
      // message more than once (ChatContainer's retry across a machine coming
      // up) passes one id for every attempt, so they are one bubble.
      clientMessageId?: string;
    },
  ) => Promise<void>;
  // A send that will not go through: its bubble becomes "Not sent", with Retry
  // and Remove (FailedSendStatus). sendMessage and startExistingChat call it
  // for a final error; a caller that gave up retrying a machine calls it too.
  markSendFailed: (chatId: string, clientMessageId: string) => void;
  // Send a failed message again, the way the composer sends. Rejects if this
  // attempt fails too (its bubble is "Not sent" again).
  retryFailedSend: (chatId: string, clientMessageId: string) => Promise<void>;
  // Drop a failed message from the transcript.
  discardFailedSend: (chatId: string, clientMessageId: string) => void;
  loadMessages: (chatId: string) => Promise<void>;
  // Fetch the next page of OLDER messages and PREPEND them to the cache.
  // Resolves to true when more history may still remain, false when the top of
  // the chat has been reached (or nothing could be loaded).
  loadOlderMessages: (chatId: string) => Promise<boolean>;
  // Merge the tool results of messages read outside this store (the React
  // Query seed and single-thread reads) into toolResultsByCallId. Known
  // entries are kept: a result is written once.
  ingestToolResultMessages: (chatId: string, messages: Message[]) => void;

  // Stream methods (chat detail events are delivered by the per-chat stream)
  processChatStreamUpdates: (chatId: string, updates: ChatUpdate[], isSnapshot?: boolean) => void;
  // Clears streaming placeholders/buffers for a chat. Called when the chat's
  // activity goes IDLE — the authoritative "nothing is streaming" signal —
  // so stale delta tails can't leave a phantom message at the end of the chat.
  clearStreamingState: (chatId: string) => void;
  // Stream resume (see chatStreamCursors). The stream records the cursor of
  // the chat it is leaving, and asks for one when entering a chat: the
  // sequence to replay from, or 0n for a full snapshot.
  recordChatStreamCursor: (chatId: string, sequence: bigint) => void;
  resumeChatStreamFrom: (chatId: string) => bigint;
  // The chat whose initial sync for the current stream subscription (a
  // snapshot, or a replay from its cached cursor) has not ended with the
  // server's chat_caught_up yet (null: none). A reconnect of an already
  // caught-up subscription does not reopen it. Drives the timeline's
  // catch-up overlay (lib/timelineCatchUp).
  chatSyncPendingId: string | null;
  setChatSyncPending: (chatId: string | null) => void;

  // Chat control methods
  cancelChat: (chatId: string) => Promise<void>;
  pauseChat: (chatId: string) => Promise<void>;
  resumeChat: (chatId: string) => Promise<void>;

  resolveQuestion: (chatId: string, questionId: string, action: string, responseData?: string) => Promise<void>;
  refreshChat: (chatId: string) => Promise<void>;
  forceResetChatToIdle: (chatId: string) => void;
  checkChatStatus: (chatId: string) => Promise<void>;
  dismissChat: (chatId: string) => Promise<void>;

  // Tool approvals + pending questions are server data owned by the React
  // Query cache (see hooks/approval-queries.ts): approve/deny via the mutation
  // hooks, read via useApprovals / usePendingApprovals / usePendingQuestions.
  // The chat stream patches that cache directly — no store-side copy here.

  // Branch chat
  _navigateToBranchedChat: (newChat: Chat, worktreeId?: string) => void;
  // Return the created chat so route-owned surfaces can open it without
  // coupling this store to browser paths or the router.
  branchChat: (chatId: string, messageId: string) => Promise<Chat | undefined>;
  // "Continue without machine": a no-machine branch that carries the
  // conversation and leaves the original chat as it is. Navigates to it.
  branchChatWithoutMachine: (chatId: string, messageId: string) => Promise<Chat>;
  // "Connect a machine": pins the chat to a machine, which ends no-machine.
  connectChatToMachine: (chatId: string, daemonId: string) => Promise<Chat>;
  branchChatToWorktree: (
    chatId: string,
    messageId: string,
    worktreeId: string,
    workspaceContext?: {
      sourceWorktreeId?: string;
      filesCopied?: string[];
      copyFilesEnabled?: boolean;
    },
  ) => Promise<Chat | undefined>;

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
        approval?: Approval;
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
// The local-only placeholder for a user message being sent, so it renders
// before the server persists it. mergeMessages retires it (by the
// "optimistic-user-" id prefix) when the persisted message arrives on the chat
// stream — which only works if the placeholder is already in the cache by then.
// seq 999998 sorts it just before the streaming message (999999).
function buildOptimisticUserMessage(
  content: string,
  attachmentIds: string[] | undefined,
  identity?: { id: string; sentAt: string },
): Message {
  const optimisticAttachments = getAttachmentsFromStore(attachmentIds || []);
  const sentAt = identity?.sentAt ?? new Date().toISOString();
  return {
    id: identity?.id ?? `optimistic-user-${Date.now()}`,
    chatId: "",
    role: MessageRole.USER,
    contentBlocks: [{ id: "", index: 0, type: ContentBlockType.TEXT, content }],
    createdAt: sentAt,
    updatedAt: sentAt,
    streamingState: StreamingState.COMPLETE,
    seq: BigInt(999998),
    thread: "",
    sequenceNumber: BigInt(0),
    attachments: optimisticAttachments.length > 0 ? optimisticAttachments : [],
  };
}

// What it takes to send a message again: everything the send was given. Kept
// from the start of a send on an existing chat until it succeeds or is
// discarded, so a send that fails can be retried as the same message. Keyed by
// client message id. In memory only: after a reload a failed message can be
// removed but not resent (canRetryFailedSend).
interface SendPayload {
  chatId: string;
  content: string;
  attachmentIds?: string[];
  options: {
    workflow?: string | null;
    workflowParams?: Record<string, unknown>;
    targetThread?: string | null;
    selectedPresets?: Record<string, string>;
  };
}
const sendPayloads = new Map<string, SendPayload>();

/** Whether a failed send can still be sent again from here. */
export function canRetryFailedSend(clientMessageId: string): boolean {
  return sendPayloads.has(clientMessageId);
}

/**
 * The chat's run is executing, so SendMessage will queue a message in the
 * thread's mailbox rather than save it (the server's test is "the workflow is
 * Active"). A run waiting on its machine or on an answer is still executing.
 */
function isRunExecuting(chatId: string): boolean {
  const activity = useActivityStore.getState().activities.get(chatId);
  return (
    activity === ChatActivity.RUNNING ||
    activity === ChatActivity.AWAITING_INPUT ||
    activity === ChatActivity.WAITING_FOR_DAEMON ||
    activity === ChatActivity.WAITING_FOR_USAGE_LIMIT
  );
}

/** Take a send's row back out of the pending-queue strip. */
function withdrawFromQueueStrip(chatId: string, thread: string, rowId: string): void {
  // The strip's one "this row is gone" path (useQueuedAgentMessages): it drops
  // the row now, and a stale read cannot bring it back. Safe because the row id
  // is never sent again — a retry mints a new one.
  initEventBus().emit("agentMailbox:drained", { chatId, thread, messageIds: [rowId] });
}

// Shared by startChat and startExistingChat: home the chat the server returned,
// flip activity to RUNNING. The optimistic first message is
// each caller's job, because the two must write it at different times.
function applyFirstSend(chat: Chat, projectId: string): void {
  const chatId = chat.id;

  // Home the new chat into the React Query caches immediately so both
  // useChat / useActiveChat and list readers (sidebar/search/worktree views)
  // show it without waiting for a stream-triggered list refetch.
  seedChatDetail(chat);
  upsertChatInListCache(projectId, chat);
  void queryClient.invalidateQueries({ queryKey: chatKeys.list(projectId) });
  void queryClient.invalidateQueries({ queryKey: chatKeys.list(null) });

  // Initialize state for the new chat
  useChatStore.getState().initChatState(chat);

  // Optimistically mark as RUNNING so the thinking indicator shows immediately
  // The backend will confirm via CHAT_ACTIVITY_CHANGED event shortly
  useActivityStore.getState().setActivity(chatId, ChatActivity.RUNNING);
}

export const useChatStore = create<ChatStoreState>((set, get) => ({
  // Initial state
  errorEvents: {},
  infoEvents: {},
  runOutputs: {},
  nodeExecutions: {},
  toolCallStates: {},
  streamingMessages: {}, // Currently streaming messages per chat+thread
  finalizedStreamIds: {}, // Delta identity: finalized assistant message ids per chat
  contextUsage: {}, // Context usage tracking for compaction indicator
  toolResultsByCallId: {}, // Normalized tool results per chat, keyed by tool_call_id
  olderLoadingByChat: {}, // Chats with a scroll-back page in flight
  activeChatId: null,
  chatSyncPendingId: null,
  hasLoaded: false,
  error: null,
  pendingStatusFetches: {},

  // Load the sidebar's chat list — the filter's project, or every project —
  // with singleflight deduplication to prevent parallel API calls.
  loadChats: async () => {
    const scope = useWorkspaceStateStore.getState().projectFilterId;

    // Use singleflight to ensure only one load per scope runs at a time
    return singleflight(`loadChats:${scope ?? "*"}`, async () => {
      set({ error: null });
      try {
        const response = await chatActions.listChats(scope ?? undefined);
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
        const serverWorkingThreads = new Map<string, number>();
        for (const chat of apiChatMap.values()) {
          serverActivities.set(chat.id, (chat.activity ?? 0) as ChatActivity);
          if (chat.workingThreads > 0) serverWorkingThreads.set(chat.id, chat.workingThreads);
        }
        useActivityStore
          .getState()
          .applyListActivities(serverActivities, lastUserUpdateSequence, serverWorkingThreads);
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

  // First send of a new chat
  startChat: async (
    worktreeId?: string,
    firstMessage: string = "Hello",
    attachmentIds?: string[],
    workflowParams?: Record<string, unknown>,
    workflow?: string | null,
    selectedPresets?: Record<string, string>,
    machine?: { daemonId?: string; noMachine?: boolean },
  ) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) {
      throw new Error("No project selected");
    }

    const effectiveWorkflowParams: Record<string, unknown> = workflowParams ?? {};

    // For new chats, ALWAYS send the explicit workflow string.
    // Backend can resolve user defaults, but we've had cases where "" ended up as Agent.
    const effectiveWorkflow = workflow ?? DEFAULT_WORKFLOW;

    // One id for this first message, however many times it is sent. A start
    // whose response was lost may well have made the chat; sending it again
    // under a new id makes a second one. The composer puts the text back when
    // a start fails, so the user's next send is the same message and must find
    // the same id: that is what the server recognizes it by.
    const attemptKey = JSON.stringify([
      "start",
      projectId,
      worktreeId ?? "",
      firstMessage,
      attachmentIds ?? [],
    ]);
    const chat = await chatActions.startChat({
      project_id: projectId,
      messages: firstMessage
        ? [{ role: MessageRole.USER, content: firstMessage }]
        : [],
      attachments: attachmentIds,
      worktree_id: worktreeId,
      daemon_id: machine?.daemonId,
      no_machine: machine?.noMachine,
      workflow: effectiveWorkflow,
      workflow_params:
        Object.keys(effectiveWorkflowParams).length > 0
          ? effectiveWorkflowParams
          : undefined,
      selectedPresets: selectedPresets,
      client_message_id: attemptIdFor(attemptKey),
    });
    endAttempt(attemptKey);

    applyFirstSend(chat, projectId);
    // Safe to seed after the request: a brand-new chat has no id until
    // StartChat returns, so nothing is subscribed to it yet and no echo can
    // beat this write. It replaces the empty list initChatState seeded.
    if (firstMessage) {
      setMessagesInCache(chat.id, [
        buildOptimisticUserMessage(firstMessage, attachmentIds),
      ]);
    }
    return chat;
  },

  startExistingChat: async (
    chatId: string,
    firstMessage: string,
    attachmentIds?: string[],
    options?: {
      workflow?: string | null;
      workflowParams?: Record<string, unknown>;
      selectedPresets?: Record<string, string>;
      clientMessageId?: string;
    },
  ) => {
    const { clientMessageId: givenId, ...sendOptions } = options ?? {};
    const workflowParams = sendOptions.workflowParams ?? {};
    const clientMessageId = givenId ?? newClientMessageId();
    const optimisticId = optimisticSendId(clientMessageId);
    sendPayloads.set(clientMessageId, {
      chatId,
      content: firstMessage,
      attachmentIds,
      options: sendOptions,
    });

    // Seed the placeholder BEFORE the request. This chat (a branch) is already
    // open and subscribed, so the server's echo of the persisted message can
    // arrive on the stream before StartChat resolves. Seeded after, the
    // placeholder lands behind its own echo, nothing ever retires it, and the
    // message renders twice until the next snapshot. Appended, not replaced:
    // the cache holds the branch's inherited history. An earlier attempt at
    // this message — a SendMessage the server refused as "not started" — is
    // replaced, not added to.
    patchMessagesCache(chatId, (msgs) => [
      ...msgs.filter(
        (m) => m.id !== optimisticId && m.id !== failedSendId(clientMessageId),
      ),
      buildOptimisticUserMessage(firstMessage, attachmentIds, {
        id: optimisticId,
        sentAt: new Date().toISOString(),
      }),
    ]);

    let chat: Chat;
    try {
      // The chat names its own project; the selected project may be a different
      // one (deep link, cross-project tab), and the server rejects a mismatch.
      chat = await chatActions.startChat({
        chat_id: chatId,
        messages: [{ role: MessageRole.USER, content: firstMessage }],
        attachments: attachmentIds,
        workflow: sendOptions.workflow ?? undefined,
        workflow_params:
          Object.keys(workflowParams).length > 0 ? workflowParams : undefined,
        selectedPresets: sendOptions.selectedPresets,
        client_message_id: clientMessageId,
      });
    } catch (error) {
      // Same as sendMessage: a machine coming up is the caller's to retry.
      if (!isDaemonConnectingError(error)) {
        get().markSendFailed(chatId, clientMessageId);
      }
      throw error;
    }
    sendPayloads.delete(clientMessageId);

    applyFirstSend(chat, chat.projectId);
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
  selectChat: (chat: Chat, options?: { deferPersist?: boolean; openEntry?: ChatOpenEntry }) => {
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

    // A real switch: the chat.open interaction runs from here to the frame
    // after its timeline caught up (ChatPresenter). Started before anything
    // below, so the RPCs this kicks off (the chat stream among them) are its
    // children; a quicker second click supersedes it.
    const cachedMessages = getMessagesFromCache(chat.id).length;
    beginChatOpen(
      chat.id,
      { "chat.cached": cachedMessages > 0, "chat.cached_messages": cachedMessages },
      options?.openEntry ?? "switch",
    );

    // Opening a different chat ABANDONS any pending new-chat compose, so its
    // scratch params die here.
    //
    // tempNewChat* previously had exactly one exit — transferTempToChat() on
    // SEND. Walking away from a half-configured compose left it populated, and
    // ChatInput restores it verbatim for the NEXT new chat (its "New chat (no
    // chatId)" branch). A `mode` picked once and never sent therefore became the
    // mode of every new chat for the rest of the session, across projects, with
    // nothing in the UI explaining why; only a reload cleared it, which is what
    // made it look intermittent. It leaks in the dangerous direction too —
    // `auto` (auto-approves tool calls) rides along exactly as easily as `plan`.
    //
    // This is the right hook precisely BECAUSE it is not a lifecycle event.
    // NewChatView's mount/unmount cannot express "abandoned": it remounts
    // between chat-creation attempts, so clearing there would wipe params the
    // user is still editing (see the standing NOTE in that component). Selecting
    // another chat is unambiguous. After a successful send this is already a
    // no-op — NewChatView calls transferTempToChat() before selectChat(), so the
    // temp state is drained by the time we get here.
    useChatParamsStore.getState().clearTempNewChatParams();

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
    // Browser viewers are filtered based on worktreeStore.currentWorktreeId

    // Save to workspace state for persistence (see persistActiveChat).
    if (!options?.deferPersist) {
      get().persistActiveChat(chat.id);
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
    // Subscribe to per-chat detail events (the per-chat stream; the user stream is untouched)
    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("subscribeToChatDetails-call");
    }
    getGlobalUpdatesStore()?.subscribeToChatDetails(chat.id);
    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("subscribeToChatDetails-end");
    }
    // The subscription is asserted, so whether the timeline is catching up
    // now speaks for this open.
    armChatOpen(chat.id);

    // The plan and its tasks, pending approvals and pending questions are
    // server data owned by the React Query cache: their hooks fetch them on
    // mount and the chat stream patches or invalidates the cache live (see
    // task-queries.ts, approval-queries.ts, question-queries.ts). No
    // store-side prefetch needed here.

    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("selectChat-end");
    }
  },

  // Record the chat as the active and most recent chat of the worktree the
  // user is viewing.
  // IMPORTANT: Always use the current worktree context, not the chat's worktree_id
  // This ensures state is saved to where the user is currently viewing (e.g., __main__)
  // so it can be properly restored on reload
  persistActiveChat: (chatId: string) => {
    const projectId = useProjectStore.getState().currentProject?.id;
    if (!projectId) return;
    const currentWorktreeId =
      useWorktreeStore.getState().currentWorktreeId ?? null;
    useWorkspaceStateStore
      .getState()
      .setActiveChatId(projectId, currentWorktreeId, chatId);
    useWorkspaceStateStore
      .getState()
      .addToChatQueue(projectId, currentWorktreeId, chatId);
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
      worktreeId ?? useWorktreeStore.getState().currentWorktreeId ?? null;

    // NOTE: viewerStore no longer has its own worktreeId state
    // The worktree context is managed by worktreeStore (single source of truth)

    if (activeChatId) {
      // An open still in flight was abandoned, not slow.
      leaveChatOpen(activeChatId);
      // Don't clean up the state, just deselect
      // State cleanup happens when chat is deleted or after timeout
      set({ activeChatId: null });

      // The stream's chat subscription is deliberately left in place. The
      // stream can only change chats by reconnecting, so dropping it here cost
      // a full reconnect now and another one to open the next chat — and
      // coming back to this chat (the commonest move from the new-chat view
      // or the mobile list) cost two, for a chat whose state never left the
      // cache. Lingering, it stays live: returning is free, and opening a
      // different chat replaces it in the one reconnect that would happen
      // anyway. evictChat releases it when the chat goes away.

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
      clientMessageId?: string;
    },
  ) => {
    if (!getChatFromCache(chatId)) {
      throw new Error(`No state for chat ${chatId}`);
    }

    // The id this message keeps wherever it is shown. If the run is
    // executing, the server queues the message under this id instead of
    // writing it to the transcript (see lib/pendingSends.ts).
    const { clientMessageId: givenId, ...sendOptions } = options ?? {};
    const clientMessageId = givenId ?? newClientMessageId();
    const optimisticId = optimisticSendId(clientMessageId);
    const sentAt = new Date().toISOString();
    const thread =
      options?.targetThread || getChatFromCache(chatId)?.workflowId || "";
    const queuedRow: QueuedRow = {
      id: clientMessageId,
      body: content,
      created_at: sentAt,
      sender_kind: AgentMessageKind.HUMAN_MESSAGE,
      attachments: attachmentIds ?? [],
    };
    sendPayloads.set(clientMessageId, { chatId, content, attachmentIds, options: sendOptions });

    // Where the message is shown while the request is in flight: where it will
    // end up. A run that is executing gets it queued, so it goes in the
    // pending-queue strip from the start (the composer's queue does the same,
    // ChatInput.handleQueue); otherwise in the transcript. The server has the
    // final word, and the response handling below moves it if this was wrong.
    const showInQueue = !!thread && isRunExecuting(chatId);

    try {
      logger.info("[sendMessage] 📤 Sending message", {
        chatId: chatId.slice(0, 8),
        content: content.slice(0, 50),
        workflow: options?.workflow,
        showInQueue,
      });

      // DON'T set busy=true optimistically - let WebSocket workflow_execution updates drive busy state
      // This prevents stuck state if backend crashes before creating workflow_execution

      notifyManager.batch(() => {
        // An earlier attempt at this message (one id across a machine wait)
        // is replaced, not added to.
        patchMessagesCache(chatId, (msgs) =>
          msgs.filter(
            (m) => m.id !== optimisticId && m.id !== failedSendId(clientMessageId),
          ),
        );
        if (showInQueue) {
          // Kept by every mailbox read until the server's row (same id) is in
          // one, so a read that lands mid-send cannot erase it.
          beginQueuedSend({ chatId, thread, row: queuedRow });
          // A resend (Retry) reuses the id of a send that was withdrawn from
          // the strip when it failed; the strip must take the id back.
          initEventBus().emit("agentMailbox:resend", { chatId, thread, messageId: clientMessageId });
          initEventBus().emit("agentMailbox:queued", { chatId, thread, message: queuedRow });
        } else {
          // Append the optimistic user message to the RQ message cache (the
          // single source of truth) BEFORE the request, so the streamed echo
          // of the persisted message always arrives after it and retires it.
          patchMessagesCache(chatId, (msgs) => [
            ...msgs,
            buildOptimisticUserMessage(content, attachmentIds, {
              id: optimisticId,
              sentAt,
            }),
          ]);
        }
      });
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

      // A pause issued just before this send must reach the server first, or
      // the send can be routed as a wake and then stranded by the pause. The
      // optimistic message is already on screen, so this wait is invisible.
      // The pause settles either way (its rejection is handled in pauseChat).
      await pausesInFlight.get(chatId);

      const rpcOptions = {
        ...(options?.workflow !== undefined && { workflow: options.workflow }),
        ...(Object.keys(workflowParams).length > 0 && {
          workflow_params: workflowParams,
        }),
        ...(options?.targetThread && { target_thread: options.targetThread }),
        ...(options?.selectedPresets &&
          Object.keys(options.selectedPresets).length > 0 && {
            selected_presets: options.selectedPresets,
          }),
        client_message_id: clientMessageId,
      };

      let response: Awaited<ReturnType<typeof chatActions.sendMessage>>;
      // In the transcript, the strip must not show the server's row a second
      // time while the request is in flight.
      if (!showInQueue) beginPendingSend(clientMessageId);
      try {
        response = await chatActions.sendMessage(
          chatId,
          content,
          attachmentIds,
          rpcOptions,
        );
      } catch (error) {
        // A machine still coming up is retried by the caller with this same
        // id (ChatContainer, sendWithDaemonWait), so the message stays as it
        // is, pending. Anything else is final: the message did not go, and
        // saying so — with a way to send it again — beats a dimmed bubble that
        // never resolves (a refused send leaves nothing on the server, #688).
        if (!isDaemonConnectingError(error)) {
          get().markSendFailed(chatId, clientMessageId);
        }
        throw error;
      } finally {
        endPendingSend(clientMessageId);
        endQueuedSend(clientMessageId);
      }
      sendPayloads.delete(clientMessageId);

      if (response.queued) {
        // Queued, not saved: the run was executing, so the message waits in
        // the thread's mailbox for its next turn and is shown in the
        // pending-queue strip, not the transcript. Drop any optimistic entry
        // and hand the row to the strip in ONE batch, so no render shows it
        // twice or not at all. (Already in the strip, this is a no-op: the
        // strip dedups by id, and the row's id is clientMessageId.)
        notifyManager.batch(() => {
          patchMessagesCache(chatId, (msgs) =>
            msgs.filter((m) => m.id !== optimisticId),
          );
          initEventBus().emit("agentMailbox:queued", {
            chatId,
            thread,
            message: { ...queuedRow, id: response.messageId },
          });
        });
      } else if (showInQueue) {
        // The run had stopped by the time the message arrived, so the server
        // saved it to the transcript after all. Move it there in one batch —
        // as an optimistic entry its echo will retire, unless the echo has
        // already arrived, in which case the transcript has it.
        notifyManager.batch(() => {
          withdrawFromQueueStrip(chatId, thread, clientMessageId);
          const echoed = getMessagesFromCache(chatId).some(
            (m) => m.id === response.messageId,
          );
          if (!echoed) {
            patchMessagesCache(chatId, (msgs) => [
              ...msgs,
              buildOptimisticUserMessage(content, attachmentIds, {
                id: optimisticId,
                sentAt,
              }),
            ]);
          }
        });
      }

      logger.debug("Message sent successfully", { chatId: chatId.slice(0, 8) });

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

  markSendFailed: (chatId: string, clientMessageId: string) => {
    const payload = sendPayloads.get(clientMessageId);
    const optimisticId = optimisticSendId(clientMessageId);
    const failedId = failedSendId(clientMessageId);
    const thread =
      payload?.options.targetThread || getChatFromCache(chatId)?.workflowId || "";
    notifyManager.batch(() => {
      // A send shown in the strip comes back to the transcript to fail: the
      // strip lists messages the agent WILL read, and this one it never will.
      if (thread) withdrawFromQueueStrip(chatId, thread, clientMessageId);
      patchMessagesCache(chatId, (msgs) => {
        const pending = msgs.find((m) => m.id === optimisticId);
        const rest = msgs.filter((m) => m.id !== optimisticId && m.id !== failedId);
        if (pending) return [...rest, { ...pending, id: failedId }];
        if (!payload) return msgs;
        return [
          ...rest,
          buildOptimisticUserMessage(payload.content, payload.attachmentIds, {
            id: failedId,
            sentAt: new Date().toISOString(),
          }),
        ];
      });
    });
  },

  retryFailedSend: async (chatId: string, clientMessageId: string) => {
    const payload = sendPayloads.get(clientMessageId);
    if (!payload) {
      // A reload drops the payload, never the text on screen: leave the entry
      // for the user to copy.
      throw new Error("This message can no longer be resent. Copy it and send it again.");
    }
    // The retry is the same send, under the same id, replacing the failed entry
    // in the same commit. The id is how the server recognizes it: a send can
    // fail on the way back (a dropped connection, a timeout) after the server
    // saved it, and an id of its own would save it twice. Under the old id the
    // server answers with the message it already has, and starts nothing.
    get().discardFailedSend(chatId, clientMessageId);
    const retryId = clientMessageId;
    // The params captured at the first attempt carry that attempt's model. The
    // composer persists every change to the chat's params, so what is stored
    // now is what the pill shows; the captured ones are only the fallback.
    const currentParams = useChatParamsStore.getState().getChatParams(chatId);
    const workflowParams =
      Object.keys(currentParams).length > 0 ? currentParams : payload.options.workflowParams;
    try {
      // The same path the composer takes (ChatContainer): routed for a chat
      // that still needs starting, and retried while its machine comes up.
      await sendWithDaemonWait({
        action: () =>
          sendOnExistingChat(
            getChatFromCache(chatId),
            chatId,
            payload.content,
            payload.attachmentIds,
            {
              ...payload.options,
              workflowParams,
              workflow: payload.options.workflow ?? null,
              clientMessageId: retryId,
            },
            {
              startExistingChat: get().startExistingChat,
              sendMessage: get().sendMessage,
            },
          ),
      });
    } catch (error) {
      // sendMessage leaves a machine-wait failure pending for its caller —
      // which is this. Out of budget, it is a failure like any other.
      if (isDaemonConnectingError(error)) get().markSendFailed(chatId, retryId);
      throw error;
    }
  },

  discardFailedSend: (chatId: string, clientMessageId: string) => {
    sendPayloads.delete(clientMessageId);
    const failedId = failedSendId(clientMessageId);
    patchMessagesCache(chatId, (msgs) => msgs.filter((m) => m.id !== failedId));
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
      const response = await chatActions.listMessages(chatId);
      // Canonical order — chat-global seq, a true total order across threads.
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
              seq: messages[0].seq,
              thread: messages[0].thread,
            }
          : null,
        lastMessage: messages[messages.length - 1]
          ? {
              id: messages[messages.length - 1].id?.slice(0, 8),
              role: messages[messages.length - 1].role,
              seq: messages[messages.length - 1].seq,
              thread: messages[messages.length - 1].thread,
            }
          : null,
      });

      const state = get();

      // PERFORMANCE: Pre-process all messages for fast rendering on tab switches
      // This is critical - without this, each ChatMessage parses JSON on every render
      // which causes 800ms+ delays when switching to chats with many messages
      const approvals =
        approvalsFromCache(chatId);
      // Build the normalized tool-result index from the loaded TOOL messages,
      // merged over anything already known for this chat, so processMessage can
      // resolve call→result the same way the live stream path does. The server
      // does not copy a result onto its call block: this index is the join.
      // A stub of a result already held whole never replaces it.
      const foundToolResults: ToolResultsByCallId = {};
      for (const message of messages) {
        if (message.role !== MessageRole.TOOL) continue;
        foldToolResultMessage(message, foundToolResults);
      }
      const loadedToolResults = mergeToolResults(
        state.toolResultsByCallId[chatId] || {},
        foundToolResults,
      );

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
      //
      // This request is unbounded (no `recent`), so it returns the whole chat —
      // assert that explicitly. Without it a stale hasMore from an earlier
      // bounded snapshot would survive and offer scroll-back that can only ever
      // come back empty.
      setMessagesInCache(chatId, messages, {
        total: response.total,
        hasMore: response.hasMore,
      });
      set({
        toolResultsByCallId: {
          ...state.toolResultsByCallId,
          [chatId]: loadedToolResults,
        },
      });
    } catch (error) {
      logger.error("[ChatStore] Failed to load messages:", error);
      reportError(error, { component: 'chat', operation: 'load_messages', chat_id: chatId });
    }
  },

  // Scroll-back paging. The initial snapshot is bounded to the newest N
  // messages, so this is how the rest of a long chat becomes reachable: fetch
  // the page immediately older than what we hold and PREPEND it.
  ingestToolResultMessages: (chatId: string, messages: Message[]) => {
    const found: ToolResultsByCallId = {};
    for (const message of messages) {
      if (message.role === MessageRole.TOOL) foldToolResultMessage(message, found);
    }
    // A result already indexed is the same one re-delivered, kept unless it
    // is a stub and this read has its body (mergeToolResults).
    const existing = get().toolResultsByCallId[chatId] || {};
    const merged = mergeToolResults(existing, found);
    if (merged === existing) return;
    set((state) => ({
      toolResultsByCallId: { ...state.toolResultsByCallId, [chatId]: merged },
    }));
  },

  loadOlderMessages: async (chatId: string) => {
    // Concurrency guard. Virtuoso can fire startReached several times in quick
    // succession (each prepend re-renders the list near the top edge), and
    // duplicate in-flight pages would race on oldestSeq and fetch the same
    // window twice.
    if (olderMessagesInFlight.has(chatId)) {
      logger.debug("[ChatStore] Older-message page already in flight", {
        chatId: chatId.slice(0, 8),
      });
      return true;
    }

    const meta = getMessagesMetaFromCache(chatId);
    // No cache entry means nothing is rendered yet — there is no scroll-back to
    // serve, and paging from seq 0 would be meaningless.
    if (!meta) return false;
    if (meta.hasMore === false) return false;

    // The paging cursor. oldestSeq is maintained by the cache helpers as
    // the MINIMUM seq we hold; the server returns messages strictly below
    // it. 0 means "unknown" — with nothing to page from, stop rather than
    // re-fetch the newest window forever.
    const beforeSeq = meta.oldestSeq ?? 0;
    if (beforeSeq <= 0) return false;

    olderMessagesInFlight.add(chatId);
    set((state) => ({
      olderLoadingByChat: { ...state.olderLoadingByChat, [chatId]: true },
    }));
    try {
      logger.debug("[ChatStore] Loading older messages", {
        chatId: chatId.slice(0, 8),
        beforeSeq,
        pageSize: OLDER_MESSAGES_PAGE_SIZE,
      });

      const response = await chatActions.listMessages(chatId, {
        recent: OLDER_MESSAGES_PAGE_SIZE,
        beforeSeq,
      });

      const older = (response.messages || []).map(
        normalizeMessageAttachmentFields,
      );

      // End of history is the server's has_more: it probes one row past the
      // page, so it is exact. The page's LENGTH cannot answer the question —
      // the server sizes a page on the transcript's rows and a page can come
      // back shorter than requested with more history below it (an older
      // server counts LLM-only HIDDEN rows and then drops them from the
      // response). Reading a short page as the end is what put "Start of
      // conversation" in the middle of a long chat and stopped scroll-back.
      //
      // A page that cannot move the cursor still ends paging whatever
      // has_more says, so no server can make this loop: an empty page, or
      // one holding nothing strictly below the cursor.
      if (older.length === 0) {
        logger.debug("[ChatStore] Reached the start of chat history", {
          chatId: chatId.slice(0, 8),
          serverHasMore: response.hasMore,
        });
        setMessagesMetaInCache(chatId, { hasMore: false });
        return false;
      }
      const movesCursor = older.some((m) => Number(m.seq) < beforeSeq);
      const reachedStart = !response.hasMore || !movesCursor;

      // The page is folded by the SDK's reducer like any other history: it is
      // prepended (the newer messages already rendered stay), its tool results
      // join the chat's index without replacing an entry the newer messages
      // depend on, and its own errors merge by id without replacing a newer
      // attempt the snapshot or the stream already delivered.
      const pageErrors = (response.errors || []).flatMap((e): ErrorUpdate[] => {
        try {
          const data = JSON.parse(e.dataJson || "{}") as Omit<ErrorUpdate, "sequence_number">;
          return [{ ...data, update_type: "error", sequence_number: Number(e.sequenceNumber) }];
        } catch {
          return [];
        }
      });
      const before = chatStateOf(chatId);
      const after = reduceChat(
        before,
        {
          kind: "olderPage",
          messages: older as unknown as SdkChatState["messages"],
          errors: pageErrors,
          total: response.total,
          hasMore: !reachedStart,
        },
        REDUCE_ENV,
      );

      // Warm the processMessage memo before the prepend commits, so the newly
      // inserted rows render from cached parses instead of parsing during the
      // scroll that requested them.
      const approvals = approvalsFromCache(chatId);
      for (const message of older) {
        getProcessedMessage(message, after.toolResults, approvals);
      }

      set((state) => ({
        toolResultsByCallId: { ...state.toolResultsByCallId, [chatId]: after.toolResults },
        ...(after.errors === before.errors
          ? {}
          : { errorEvents: { ...state.errorEvents, [chatId]: after.errors } }),
      }));

      // prependMessagesCache recomputes oldestSeq from the merged list, so
      // the next page's cursor moves strictly backward.
      prependMessagesCache(chatId, older, {
        total: response.total,
        hasMore: !reachedStart,
      });

      logger.info("[ChatStore] Prepended older messages", {
        chatId: chatId.slice(0, 8),
        loaded: older.length,
        reachedStart,
      });

      return !reachedStart;
    } catch (error) {
      logger.error("[ChatStore] Failed to load older messages:", error);
      reportError(error, { component: "chat", operation: "load_older_messages", chat_id: chatId });
      // Leave hasMore alone: this is a transient failure, not end-of-history.
      // The next startReached retries.
      return true;
    } finally {
      olderMessagesInFlight.delete(chatId);
      set((state) => {
        if (!state.olderLoadingByChat[chatId]) return state;
        const { [chatId]: _done, ...rest } = state.olderLoadingByChat;
        return { olderLoadingByChat: rest };
      });
    }
  },


  // Process chat detail events from the per-chat stream.
  // Called by globalUpdatesStore when per-chat events arrive.
  //
  // What a batch DOES to the chat — the transcript, the tool results, the live
  // tool statuses, the streaming placeholders, the finalized-id set and the
  // error / info / output logs — is @reliantlabs/sdk's reduceChat. This action
  // assembles the chat's state from the store's slices, runs the batch through
  // it, commits the result, and does what only the app does around it:
  // coalescing content deltas onto an animation frame, the refetch signals, the
  // error-marker routing (the upgrade modal), and projecting approvals,
  // questions and threads into the caches the UI reads them from.
  processChatStreamUpdates: (chatId: string, updates: ChatUpdate[], isSnapshot = false) => {
    // Refetch signals from the chat stream (e.g. workflow_executions,
    // plan_tasks). After decodeChatUpdate the JSON data is spread into the
    // update, so { type: "workflow_executions" } is a top-level field.
    for (const u of updates) {
      if (u.update_type !== "refetch") continue;
      const refetchType = (u as unknown as { type: string }).type as RefetchType;
      if (!refetchType) continue;
      triggerRefetch(refetchType, chatId);
      // The plan and its tasks are on the generated hooks: the signal marks
      // their reads stale directly.
      if (refetchType === "plan_tasks") invalidatePlanTasks();
    }

    // Messages of another chat never belong here: a stale frame from a chat
    // the stream has since left must not contaminate this one.
    const messageUpdates = updates
      .filter((u): u is ProtoMessageUpdate => u.update_type === "message" && "message" in u)
      .map((u) => u.message)
      .filter((msg) => {
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
    const messages: Message[] = messageUpdates.map(convertProtoMessage);

    const approvalUpdates = updates.filter((u): u is ToolApprovalUpdate => u.update_type === "approval");
    const threadUpdates = updates.filter((u): u is ActiveThreadUpdate => u.update_type === "thread");
    const workflowStatusUpdates = updates.filter((u): u is WorkflowStatusUpdate => u.update_type === "workflow_status");
    const errorUpdates = updates.filter((u): u is ErrorUpdate => u.update_type === "error");
    const infoUpdates = updates.filter((u): u is InfoUpdate => u.update_type === "info" || u.update_type === "warning");
    const runOutputUpdates = updates.filter((u): u is RunOutputUpdate => u.update_type === "run_output");
    const nodeExecutionUpdates = updates.filter((u): u is NodeExecutionUpdate => u.update_type === "node_execution");
    const questionUpdates = updates.filter((u): u is QuestionUpdate => u.update_type === "question");
    const toolCallUpdates = updates.filter((u): u is ToolCallUpdate => u.update_type === "tool_call");
    const rawStreamingDeltas = updates.filter((u): u is StreamingDelta => u.update_type === "streaming_delta");
    // Emitted exactly once per allocated assistant message id when its stream
    // ends (success/abort/cancel), riding the persisted+sequenced channel.
    const streamFinalizedUpdates = updates.filter((u): u is StreamFinalizedUpdate => u.update_type === "stream_finalized");

    // THE DROP RULE, for the frame buffer: a delta stamped with a finalized id
    // is a stale tail and must not even be buffered. (The reducer applies the
    // same rule to what it is handed; this one keeps the buffer clean.)
    const batchFinalizedIds = new Set<string>();
    for (const u of streamFinalizedUpdates) if (u.message_id) batchFinalizedIds.add(u.message_id);
    const isFinalizedId = (id: string | undefined): boolean => isFinalizedStreamId(chatId, id);
    const liveStreamingDeltas = rawStreamingDeltas.filter(
      (d) => !(isFinalizedId(d.message_id) || (d.message_id ? batchFinalizedIds.has(d.message_id) : false)),
    );

    // Content deltas are coalesced onto one animation frame per thread; what
    // comes back is what must be folded now.
    const streamingDeltas = bufferStreamingDeltas(chatId, liveStreamingDeltas, (deltas, thread) =>
      flushBufferedDeltas(chatId, deltas, thread),
    );

    // PERFORMANCE: a batch of only streaming deltas that were all buffered has
    // nothing to commit; the buffered flush will.
    const hasNonStreamingUpdates =
      messages.length > 0 ||
      approvalUpdates.length > 0 ||
      threadUpdates.length > 0 ||
      workflowStatusUpdates.length > 0 ||
      errorUpdates.length > 0 ||
      infoUpdates.length > 0 ||
      runOutputUpdates.length > 0 ||
      nodeExecutionUpdates.length > 0 ||
      toolCallUpdates.length > 0 ||
      // stream_finalized markers must reach the commit below so the
      // finalized-id set is seeded and any matching placeholder retired.
      streamFinalizedUpdates.length > 0;
    if (!hasNonStreamingUpdates && rawStreamingDeltas.length > 0 && streamingDeltas.length === 0) return;

    if (workflowStatusUpdates.length > 0) {
      logger.debug("[WorkflowStatus] Chat stream updates received", {
        chatId: chatId.slice(0, 8),
        count: workflowStatusUpdates.length,
        statuses: workflowStatusUpdates.map((u) => u.status),
      });
    }

    const cachedMessages = getMessagesFromCache(chatId);
    if (isSnapshot) {
      logger.info(
        snapshotReplacesMessages(cachedMessages, messages)
          ? "[ChatStore] Snapshot: replacing messages for chat"
          : "[ChatStore] Snapshot: merging (preserving loaded older pages)",
        { chatId: chatId.slice(0, 8), snapshotCount: messages.length, previousCount: cachedMessages.length },
      );
    }

    // Fold. The deltas the reducer sees are the ones to act on now, preceded by
    // the stale tails it drops itself but reads for the abort pass (a marker
    // can ride the same batch as the deltas it ends). The state that only the
    // app projects elsewhere (approvals, questions, threads) is not handed in.
    // The batch keeps its server order: what the adapter hands over is the
    // stream as it arrived, minus what it holds back, never regrouped by kind.
    // Deltas buffered by an EARLIER batch and released by this one came first.
    const before = chatStateOf(chatId);
    const liveSet = new Set<StreamingDelta>(liveStreamingDeltas);
    const releasedNow = new Set<StreamingDelta>(streamingDeltas);
    const convertedByUpdate = new Map<ProtoMessageUpdate["message"], Message>(
      messageUpdates.map((msg, i) => [msg, messages[i]!]),
    );
    const inBatch = new Set<ChatUpdate>(updates);
    const folded: ChatUpdate[] = streamingDeltas.filter((d) => !inBatch.has(d));
    for (const u of updates) {
      switch (u.update_type) {
        case "message": {
          const message = "message" in u ? convertedByUpdate.get((u as ProtoMessageUpdate).message) : undefined;
          if (message) folded.push({ update_type: "message", message } as unknown as ChatUpdate);
          break;
        }
        case "streaming_delta":
          // A stale tail is read for the abort pass; a live delta goes in only
          // if the frame buffer released it now (the rest flush on the frame).
          if (!liveSet.has(u as StreamingDelta) || releasedNow.has(u as StreamingDelta)) folded.push(u);
          break;
        case "tool_call":
        case "error":
        case "info":
        case "warning":
        case "run_output":
        case "node_execution":
        case "stream_finalized":
          folded.push(u);
          break;
      }
    }
    const after = reduceChat(
      before,
      isSnapshot
        ? { kind: "snapshot", updates: folded, cursor: before.cursor, keepStreaming: true }
        : { kind: "updates", updates: folded, cursor: before.cursor },
      REDUCE_ENV,
    );

    // Route on any chat-error marker tail planted by the workflow / driver
    // (RELIANT_MANAGED_QUOTA_EXHAUSTED, RELIANT_DAEMON_OFFLINE_HALT, …).
    // Live-only side effects (the modal) skip a snapshot; marker-stripping is
    // idempotent and runs in both modes so the timeline is clean either way.
    // It edits the update in place, and the state holds that same update.
    errorUpdates.forEach((errorUpdate) => routeChatErrorMarker(errorUpdate, isSnapshot));

    const state = get();

    // Commit the Zustand slices in one shot. A slice a batch does not touch
    // keeps its identity: every rendered ChatMessage subscribes to the
    // tool-result index and re-parses when it changes reference.
    set({
      toolResultsByCallId: { ...state.toolResultsByCallId, [chatId]: after.toolResults },
      errorEvents: { ...state.errorEvents, [chatId]: after.errors },
      infoEvents: { ...state.infoEvents, [chatId]: after.info },
      runOutputs: { ...state.runOutputs, [chatId]: after.runOutputs },
      nodeExecutions: { ...state.nodeExecutions, [chatId]: after.nodeExecutions },
      toolCallStates: { ...state.toolCallStates, [chatId]: after.toolCalls as Map<string, ToolCallState> },
      // NOTE: chat activity is NOT updated here — handleChatActivityChanged()
      // in globalUpdatesStore.ts populates activityStore from the global
      // stream. Nor is contextUsage: it arrives via handleChatContextUsage.
      streamingMessages:
        after.streaming === before.streaming
          ? state.streamingMessages
          : {
              ...state.streamingMessages,
              [chatId]: Object.keys(after.streaming).length > 0 ? (after.streaming as Record<string, Message>) : undefined,
            },
      finalizedStreamIds:
        after.finalizedStreamIds === before.finalizedStreamIds
          ? state.finalizedStreamIds
          : { ...state.finalizedStreamIds, [chatId]: after.finalizedStreamIds as Set<string> },
    });

    // ---- External writes (post-commit) ----
    // Everything below writes to stores OTHER than this one (RQ caches,
    // threadActivityStore). They run after the commit but in the same
    // synchronous task, so React batches all of it into one render.

    // The merged, canonically ordered transcript, to the RQ cache (the single
    // source of truth for a chat's messages).
    setMessagesInCache(chatId, after.messages as unknown as Message[]);

    // Self-heal a stranded streaming placeholder. A tool call whose input never
    // finished streaming renders as "Preparing..." forever, and cancelling at
    // exactly that moment leaves one behind that outlives every reload (a
    // snapshot rebuilds the transcript but never touched the streaming slice).
    // A snapshot for a chat the server reports as NOT running is authoritative
    // that nothing is streaming: whatever is still in the slice is a tail that
    // will never complete.
    if (isSnapshot) {
      const activity = useActivityStore.getState().activities.get(chatId);
      const chatIsIdle = !isBusyActivity(activity);
      if (chatIsIdle && get().streamingMessages[chatId]) {
        logger.info("[Streaming] Snapshot for an idle chat — dropping stranded streaming placeholder", {
          chatId: chatId.slice(0, 8),
        });
        get().clearStreamingState(chatId);
      }
      // A snapshot carries no node state and replaced the live node events: an
      // open run-status view re-reads where its run stands, or the events the
      // snapshot skipped past would be missing.
      void queryClient.invalidateQueries({
        queryKey: chatServiceKeys.getWorkflowNodeExecutions({ chatId }),
      });
    }

    // Keep any open thread-scoped reader (a spawn preview, a selected thread)
    // current. Only this batch's messages are fanned out, not the whole merged
    // array: the thread caches own their own history, and the stream's job is
    // to add to it.
    fanOutMessagesToThreadCaches(chatId, messages);

    // Approval updates patch the React Query cache directly (setQueryData, no
    // refetch): approvals are server data owned by the cache, read via
    // useApprovals / usePendingApprovals, which derives "pending" from the same
    // list, so one upsert updates both.
    approvalUpdates.forEach((approvalUpdate) => applyApprovalUpdate(chatId, approvalUpdate));

    // Question updates patch the chat's pending-question list: "pending" adds
    // that question; "resolved" removes THAT question only — other threads'
    // questions are still waiting. An async question patches the timeline's
    // async list instead.
    for (const qu of questionUpdates) applyQuestionUpdate(chatId, qu);

    // Thread updates live in threadActivityStore (not chatStore).
    if (threadUpdates.length > 0) {
      const threadActivityState = useThreadActivityStore.getState();
      threadActivityState.setThreads(
        chatId,
        mergeActiveThreads(threadActivityState.threads[chatId] || [], threadUpdates),
      );
    }

    // Warm the reference-keyed processMessage memo so the render layer reads
    // parses instead of making them. Rendering invalidation is the memo's own
    // (keyed on message / tool-result index / approvals references), so there
    // is no store-side parsed cache to keep in sync.
    const messagesToProcess: Message[] = [...messages];
    for (const [threadKey, placeholder] of Object.entries(after.streaming)) {
      if (before.streaming[threadKey] !== placeholder) messagesToProcess.push(placeholder as unknown as Message);
    }

    // A tool result usually arrives in a LATER batch than the assistant tool
    // call it belongs to. That assistant message is already stored (not in this
    // batch's `messages`): warm the ones whose calls this batch's results
    // resolve.
    if (after.toolResults !== before.toolResults) {
      const resolvedIds = new Set(
        Object.keys(after.toolResults).filter((id) => before.toolResults[id] !== after.toolResults[id]),
      );
      if (resolvedIds.size > 0) {
        const alreadyQueued = new Set(messagesToProcess.map((m) => m.id));
        for (const msg of after.messages as unknown as Message[]) {
          if (msg.role !== MessageRole.ASSISTANT || alreadyQueued.has(msg.id)) continue;
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
    }
    // Approvals live in the React Query cache; read the current list
    // (including this batch's upserts above) to embed approval status into
    // processed tool calls.
    const approvalsForProcessing = approvalsFromCache(chatId);
    messagesToProcess.forEach((message) => {
      getProcessedMessage(message, after.toolResults, approvalsForProcessing);
    });

    // A complete assistant message ends its thread's stream. Clear that
    // thread's frame buffer, or a flush timer firing after the message would
    // recreate a partial placeholder over the complete message.
    const completedThreads = new Set<string>();
    for (const m of messages) {
      if (m.role === MessageRole.ASSISTANT && isProtoMessageComplete(m)) {
        completedThreads.add(normalizeThreadKey(chatId, m.thread));
      }
    }
    for (const threadKey of completedThreads) clearStreamingBuffer(chatId, threadKey);
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

  recordChatStreamCursor: (chatId: string, sequence: bigint) => {
    // A cursor without cached state to stand behind it would replay updates
    // onto an empty list and render a fragment as the whole chat.
    if (sequence > 0n && hasMessagesCache(chatId)) {
      chatStreamCursors.set(chatId, sequence);
    } else {
      chatStreamCursors.delete(chatId);
    }
  },

  resumeChatStreamFrom: (chatId: string) => {
    const cursor = chatStreamCursors.get(chatId);
    if (cursor === undefined) return 0n;
    // Consumed on use: from here the stream owns the cursor, and it is
    // recorded afresh when the stream leaves the chat. Leaving a stale copy
    // here would let a later reopen rewind to it.
    chatStreamCursors.delete(chatId);
    if (!hasMessagesCache(chatId)) return 0n;
    // An optimistic user message is local-only: no cursor describes it. A
    // replay retires it only when the real message arrives, so one stranded
    // by a send that never answered would outlive the reopen. A snapshot
    // replaces it. (A send that failed is "optimistic-failed-*" instead, and
    // both a replay and a snapshot keep it, with its Retry.)
    if (
      getMessagesFromCache(chatId).some((m) =>
        m.id.startsWith("optimistic-user-"),
      )
    ) {
      return 0n;
    }
    return cursor;
  },

  setChatSyncPending: (chatId: string | null) => {
    // Recorded as the stream reports it, whatever is cached: whether the
    // timeline has anything to cover is the timeline's call, and it changes
    // as the cache fills (lib/timelineCatchUp).
    if (get().chatSyncPendingId === chatId) return;
    set({ chatSyncPendingId: chatId });
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

    // Drop any half-arrived streaming placeholder this cancel just orphaned.
    // A tool call whose input never finished streaming renders as
    // "Preparing..." forever (the renderers key that state off
    // `input === undefined`), and nothing else reliably cleans it up:
    // the only other caller of clearStreamingState is the IDLE
    // activity-changed handler, which is a TRANSIENT event. Cancelling at the
    // moment a tool call is streaming — and any later reload — leaves the
    // placeholder with no second chance to be cleared, so it sticks to the
    // transcript until the slice is evicted.
    //
    // Clearing here makes the cancel self-sufficient rather than relying on
    // catching one event. It is also the optimistic half of the same update
    // the IDLE handler performs, so the two agree.
    get().clearStreamingState(chatId);
    // Set the chat IDLE in the RQ caches (list + detail).
    patchChatCaches(projectId, chatId, { state: ChatState.IDLE });

    // Also update activityStore so sidebar dot reflects change immediately
    useActivityStore.getState().setActivity(chatId, ChatActivity.IDLE);

    // Then make the API call to actually cancel the workflow
    try {
      await chatActions.cancelChat(chatId);
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

    // Drop any half-arrived streaming placeholder this pause just orphaned.
    //
    // Same defect as cancelChat, reached by a different door: a tool call
    // whose input was mid-stream when the pause landed has no input, and the
    // renderers key "Preparing..." off `input === undefined`, so it renders
    // forever. Pausing is in fact the EASIER case to hit — a pause is
    // something a user does deliberately while watching work happen, which is
    // exactly when a tool call is likely to be streaming.
    //
    // The snapshot self-heal in processChatStreamUpdates cannot rescue this
    // one: it only fires for a chat the server reports as NOT running, and a
    // paused chat's workflow is still very much alive (status RUNNING in
    // Temporal, with a pending activity). Refusing to clear live streaming
    // state on a running chat is correct there — a mid-run reconnect must not
    // blank a genuinely streaming tool call — so the pause has to clean up
    // after itself, exactly as the cancel does.
    get().clearStreamingState(chatId);

    // Make the API call to actually pause the workflow. Registered so a send
    // issued after this pause waits for it (see pausesInFlight).
    const pausing = chatActions.pauseChat(chatId).then(
      () => undefined,
      (error) => {
        logger.error("Failed to pause chat:", error);
        // Revert optimistic update on error
        get().refreshChat(chatId);
      },
    );
    pausesInFlight.set(chatId, pausing);
    try {
      await pausing;
    } finally {
      if (pausesInFlight.get(chatId) === pausing) pausesInFlight.delete(chatId);
    }
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

    // Also update activityStore so sidebar dot reflects change immediately
    useActivityStore.getState().setActivity(chatId, ChatActivity.RUNNING);

    // Make the API call to actually resume the workflow
    try {
      await chatActions.resumeChat(chatId);
    } catch (error) {
      logger.error("Failed to resume chat:", error);
      // Revert optimistic update on error
      get().refreshChat(chatId);
    }
  },

  resolveQuestion: async (chatId, questionId, action, responseData) => {
    await resolvePendingQuestion(chatId, questionId, action, responseData);
  },

  // Refresh chat - re-asserts the chat stream
  refreshChat: async (chatId: string) => {
    // Re-subscribing restores a dropped chat stream (resuming from its
    // cursor); it is a no-op while the stream is already up.
    getGlobalUpdatesStore()?.subscribeToChatDetails(chatId);
  },


  // Force reset a stuck chat to idle state
  // This is a recovery function for chats that are stuck in busy state
  // It clears all running state without making API calls
  forceResetChatToIdle: (chatId: string) => {
    logger.warn("⚠️ Force resetting chat to idle state (recovery mode):", {
      chatId: chatId.slice(0, 8),
    });

    // The edits below diverge local state from the server's, so it no longer
    // matches any cursor — the next open must take a snapshot.
    chatStreamCursors.delete(chatId);

    // Clear thread activity in the dedicated store
    useThreadActivityStore.getState().clearThreads(chatId);
    // Approvals are server data in the RQ cache; reconcile from the server.
    queryClient.invalidateQueries({ queryKey: approvalServiceKeys.all });

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
      const chat = await chatActions.getChat(chatId);

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
        .applyChatSnapshot(chatId, serverActivity, baselineSeq, chat.workingThreads ?? 0);
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
      const { chat: newChat } = await chatActions.branchChat(chatId, {
        messageId,
      });

      // Home the branched chat into the React Query detail cache immediately.
      seedChatDetail(newChat);

      // Initialize state for new chat
      get().initChatState(newChat);

      // Select the new chat
      get().selectChat(newChat);

      // Preserve desktop navigation; route-owned surfaces use the returned
      // chat to update their URL after creation has succeeded.
      get()._navigateToBranchedChat(newChat);
      return newChat;
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

  branchChatWithoutMachine: async (chatId: string, messageId: string) => {
    const { chat: newChat } = await chatActions.branchChat(chatId, {
      messageId,
      noMachine: true,
    });
    seedChatDetail(newChat);
    upsertChatInListCache(newChat.projectId, newChat);
    get().initChatState(newChat);

    get().selectChat(newChat);
    get()._navigateToBranchedChat(newChat, newChat.worktreeId);
    trackEvent("chat_branched_without_machine", {});
    return newChat;
  },

  connectChatToMachine: async (chatId: string, daemonId: string) => {
    const updated = await chatActions.setChatDaemon(chatId, daemonId);
    // The response is the connected chat (no_machine cleared). Install it now
    // rather than waiting on chat_config_changed, so the pill, the composer
    // hint and the request_machine card all flip on this render.
    seedChatDetail(updated);
    patchChatCaches(updated.projectId, chatId, {
      noMachine: updated.noMachine,
      activeDaemonId: updated.activeDaemonId,
    });
    trackEvent("chat_connected_machine", {});
    return updated;
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
      const { chat: newChat } = await chatActions.branchChat(chatId, {
        messageId,
        worktreeId,
      });

      // Home the branched chat into the React Query detail cache immediately.
      seedChatDetail(newChat);

      // Initialize state for new chat
      get().initChatState(newChat);

      // Select the new chat
      get().selectChat(newChat);

      // Route-owned surfaces can navigate independently of creation errors.
      get()._navigateToBranchedChat(newChat, worktreeId);
      return newChat;
    } catch (error) {
      logger.error("Failed to branch chat to worktree:", error);
      throw error;
    }
  },

  // Connection restored - streaming is managed by globalUpdatesStore
  onConnectionRestored: () => {
    set({ error: null });
    // Both streams reconnect on their own (globalUpdatesStore)
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
      await chatActions.cancelToolCall(toolCallId);
      logger.info("Tool call cancelled successfully", toolCallId);
    } catch (error) {
      logger.error("Failed to cancel tool call", toolCallId, error);
      throw error;
    }
  },

  convertToBackground: async (_chatId: string, toolCallId: string) => {
    try {
      const result = await chatActions.convertToolCallToBackground(toolCallId);
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
      const result = await chatActions.dismissChat(chatId);

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
    return isBusyActivity(useActivityStore.getState().activities.get(chatId));
  },

  // Release all retained state for one chat. The messages cache and the
  // per-chat slices below have no TTL (gcTime: Infinity / plain records), so
  // deleted/archived chats must be evicted explicitly or they leak until
  // logout. Chat objects themselves (chatKeys.list/detail) are removed by
  // removeChatFromListCache in the delete path; this handles everything else.
  evictChat: (chatId: string) => {
    // A chat left on screen keeps its stream subscription (clearCurrentChat),
    // so an archived or deleted one may still hold it. Release it: an archived
    // chat has nothing left to stream, and a deleted one would get every
    // future reconnect refused. A no-op unless this chat is the subscribed one.
    getGlobalUpdatesStore()?.unsubscribeFromChatDetails(chatId);
    for (const [clientMessageId, payload] of sendPayloads) {
      if (payload.chatId === chatId) sendPayloads.delete(clientMessageId);
    }
    clearStreamingBuffersForChat(chatId);
    clearMessagesCache(chatId);
    // The execution tree is retained exactly as long as the messages (gcTime:
    // Infinity, see useWorkflowExecutions), so it is released with them.
    queryClient.removeQueries({ queryKey: workflowExecutionsOfChat(chatId) });
    // A page may still be in flight for the evicted chat; its finally-block
    // delete would otherwise be the only thing clearing this, and an aborted
    // teardown would leave a tombstone that blocks paging if the chat returns.
    olderMessagesInFlight.delete(chatId);
    // The cursor describes the state being dropped here; without it a reopen
    // must start from a snapshot.
    chatStreamCursors.delete(chatId);
    useThreadActivityStore.getState().clearThreads(chatId);

    const omit = <T>(record: Record<string, T>): Record<string, T> => {
      if (!(chatId in record)) return record;
      const { [chatId]: _evicted, ...rest } = record;
      return rest;
    };
    set((state) => ({
      errorEvents: omit(state.errorEvents),
      infoEvents: omit(state.infoEvents),
      runOutputs: omit(state.runOutputs),
      nodeExecutions: omit(state.nodeExecutions),
      toolCallStates: omit(state.toolCallStates),
      streamingMessages: omit(state.streamingMessages),
      finalizedStreamIds: omit(state.finalizedStreamIds),
      toolResultsByCallId: omit(state.toolResultsByCallId),
      olderLoadingByChat: omit(state.olderLoadingByChat),
      contextUsage: omit(state.contextUsage),
    }));
  },

  // Reset store to initial state (for logout)
  reset: () => {
    // Clear all streaming buffers (safety net for any orphaned buffers)
    // Note: streamingBuffers is keyed by "chatId:thread", so we clear all
    for (const buffer of streamingBuffers.values()) {
      cancelBufferFlush(buffer);
    }
    streamingBuffers.clear();

    // Unsubscribe from chat details (closes the chat stream)
    try {
      getGlobalUpdatesStore()?.unsubscribeFromChatDetails();
    } catch {
      // May fail during teardown
    }

    // Clear thread activity store
    useThreadActivityStore.getState().clearAll();

    olderMessagesInFlight.clear();
    chatStreamCursors.clear();
    sendPayloads.clear();

    // Drop the homed Chat objects from the React Query caches (the single
    // source of truth) so a logout clears chat data everywhere.
    queryClient.removeQueries({ queryKey: chatKeys.all });
    queryClient.removeQueries({ queryKey: chatServiceKeys.archivedChat });
    // Messages are homed in the RQ cache — drop all their entries too.
    clearAllMessagesCache();

    // Reset to initial state
    set({
      errorEvents: {},
      infoEvents: {},
      runOutputs: {},
      nodeExecutions: {},
      streamingMessages: {},
      finalizedStreamIds: {},
      contextUsage: {},
      toolResultsByCallId: {},
      olderLoadingByChat: {},
      toolCallStates: {},
      activeChatId: null,
      chatSyncPendingId: null,
      hasLoaded: false,
      error: null,
      pendingStatusFetches: {},
    });
  },
}));

registerToolResultSink((chatId, messages) =>
  useChatStore.getState().ingestToolResultMessages(chatId, messages),
);
