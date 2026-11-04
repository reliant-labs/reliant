// Copyright (c) 2025 Reliant Labs
// gRPC client for one chat's detail stream (StreamChatUpdates): its snapshot
// or replay, the caught_up marker, then live messages, approvals, tool calls
// and streaming deltas. One connection at a time, for the chat on screen.
//
// It is separate from the user-wide stream (streaming-grpc.ts) on purpose: a
// chat switch cancels and reopens only this stream, so it never pays for a new
// user stream, its global replay, or the user-level events that would stall
// behind it. The two reconnect independently, and a chat this stream cannot
// serve never takes the user stream down.

import { Code, ConnectError } from "@connectrpc/connect";
import { create, fromJsonString } from "@bufbuild/protobuf";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import { tabSwitchProfiler } from "../lib/tabSwitchProfiler";
import { ReconnectingStream } from "./stream-connection";
import { convertUserUpdateData } from "./streaming-grpc";
import {
  StreamingService,
  StreamChatUpdatesRequestSchema,
  ChatUpdateType,
  type ChatStreamEvent,
  type ChatUpdateData,
} from "../gen/reliant/v1/streaming_pb";
import { MessageSchema } from "../gen/reliant/v1/chat_pb";
import type {
  ChatStreamCallbacks,
  ChatUpdate,
  ContextUsageInfo,
  ProtoMessageUpdate,
} from "../types/streaming";

// Log prefix for filtering/debugging
const LOG_PREFIX_STREAM = "[📡 ChatStream]";

// Chat gap-resync pacing. Chat gaps are exact (per-chat sequences are
// contiguous and never filtered), so the interval only guards against
// reconnect loops.
const CHAT_GAP_RESYNC_MIN_INTERVAL_MS = 1_000;

/**
 * The server refused the chat: StreamChatUpdates answers NotFound for a chat
 * that no longer exists or belongs to someone else (both deliberately
 * indistinguishable), and for nothing else during setup.
 */
function isChatRefused(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.NotFound;
}

// ============================================================================
// Type Converters
// ============================================================================

// Map from proto ChatUpdateType enum to string update_type discriminator
const CHAT_UPDATE_TYPE_MAP: Record<number, string> = {
  [ChatUpdateType.MESSAGE]: "message",
  [ChatUpdateType.APPROVAL]: "approval",
  [ChatUpdateType.THREAD]: "thread",
  [ChatUpdateType.TOOL_CALL]: "tool_call",
  [ChatUpdateType.WORKFLOW_STATUS]: "workflow_status",
  [ChatUpdateType.ERROR]: "error",
  [ChatUpdateType.CHAT]: "chat",
  [ChatUpdateType.RUN_OUTPUT]: "run_output",
  [ChatUpdateType.NODE_EXECUTION]: "node_execution",
  [ChatUpdateType.EXECUTION_LOG]: "execution_log",
  [ChatUpdateType.WORKFLOW_EXECUTION]: "workflow_execution",
  [ChatUpdateType.INFO]: "info",
  [ChatUpdateType.WARNING]: "warning",
  [ChatUpdateType.REFETCH]: "refetch",
  [ChatUpdateType.STREAMING_DELTA]: "streaming_delta",
  [ChatUpdateType.QUESTION]: "question",
  [ChatUpdateType.STREAM_FINALIZED]: "stream_finalized",
  [ChatUpdateType.AGENT_MESSAGES_DRAINED]: "agent_messages_drained",
};

// Valid update types that we expect from the backend
const VALID_UPDATE_TYPES = new Set([
  "message",
  "approval",
  "thread",
  "workflow_status",
  "tool_call",
  "error",
  "info",
  "warning",
  "chat",
  "streaming_delta",
  "run_output",
  "node_execution",
  "execution_log",
  "workflow_execution",
  "refetch",
  "question",
  "stream_finalized",
  "agent_messages_drained",
]);

/**
 * Validate that an update has the required fields for its type
 */
function isValidChatUpdate(
  updateType: string,
  data: Record<string, unknown>,
): boolean {
  if (!VALID_UPDATE_TYPES.has(updateType)) {
    return false;
  }

  switch (updateType) {
    case "message": {
      const wrappedMessage = data.message as Record<string, unknown> | undefined;
      if (wrappedMessage) {
        return (
          typeof wrappedMessage.id === "string" &&
          (typeof wrappedMessage.role === "string" ||
            typeof wrappedMessage.role === "number")
        );
      }
      return (
        typeof data.id === "string" &&
        (typeof data.role === "string" || typeof data.role === "number")
      );
    }
    case "approval":
      return typeof data.id === "string" && typeof data.status === "string";
    case "thread":
      return typeof data.id === "string" && typeof data.status === "string";
    case "workflow_status":
      return (
        typeof data.workflow_id === "string" || typeof data.status === "string"
      );
    case "tool_call":
      return (
        typeof data.tool_call_id === "string" || typeof data.id === "string"
      );
    case "error":
      return (
        typeof data.error_message === "string" ||
        typeof data.activity_type === "string"
      );
    case "info":
    case "warning":
      return typeof data.id === "string" && typeof data.message === "string";
    case "chat":
      return typeof data.id === "string" || typeof data.chat_id === "string";
    case "streaming_delta":
      return typeof data.delta_type === "string";
    case "stream_finalized":
      // The delta identity protocol keys everything off the pre-allocated
      // message id; a marker without one is unusable.
      return typeof data.message_id === "string";
    case "agent_messages_drained":
      // The ids ARE the payload — an announcement without them cannot retire
      // anything from the pending-queue strip.
      return Array.isArray(data.message_ids);
    default:
      return true;
  }
}

/**
 * Convert gRPC ChatUpdateData to ChatUpdate format.
 * ChatUpdateData uses a dataJson field containing the actual update data.
 * The JSON data uses snake_case because it comes from Go's encoding/json.
 */
export function convertChatUpdateData(update: ChatUpdateData): ChatUpdate | null {
  try {
    const dataJson = update.dataJson || "{}";
    const data = JSON.parse(dataJson) as Record<string, unknown>;

    const updateTypeStr = CHAT_UPDATE_TYPE_MAP[update.updateType];
    if (!updateTypeStr) {
      logger.warn(`${LOG_PREFIX_STREAM} Missing update_type in update`);
      return null;
    }

    if (!isValidChatUpdate(updateTypeStr, data)) {
      logger.warn(`${LOG_PREFIX_STREAM} Update missing expected fields`, {
        updateType: updateTypeStr,
        entityId: update.entityId?.slice(0, 8),
        dataKeys: Object.keys(data),
      });
    }

    if (updateTypeStr === "message") {
      const messagePayload =
        data && typeof data.message === "object" && data.message !== null
          ? (data.message as Record<string, unknown>)
          : data;
      const parsedMessage = fromJsonString(
        MessageSchema,
        JSON.stringify(messagePayload),
        {
          ignoreUnknownFields: true,
        },
      );
      return {
        update_type: "message",
        message: parsedMessage,
      };
    }

    // Remove update_type from data before spreading — the Go backend serializes
    // UpdateType as an integer (proto enum), which would overwrite the correct
    // string discriminator derived from CHAT_UPDATE_TYPE_MAP above.
    delete data.update_type;

    return {
      update_type: updateTypeStr,
      sequence_number: Number(update.sequenceNumber),
      ...data,
    } as ChatUpdate;
  } catch (error) {
    logger.error(`${LOG_PREFIX_STREAM} Failed to parse update data`, {
      updateType: CHAT_UPDATE_TYPE_MAP[update.updateType],
      entityId: update.entityId?.slice(0, 8),
      error: error instanceof Error ? error.message : String(error),
    });
    return null;
  }
}


/**
 * The context usage carried by the latest main-thread message in `updates`,
 * or null when none carries one.
 *
 * Persisted message updates carry the thread's token count and the compaction
 * threshold of the model that produced it (internal/threads/save_message.go),
 * the same pair a snapshot reports. Only the main thread's — the chat's own,
 * keyed by the chat id, which is the root workflow's thread id — drives the
 * chat-level indicator; a spawned thread's context is its own.
 */
export function latestMainThreadContextUsage(
  updates: ChatUpdateData[],
  chatId: string | undefined,
): ContextUsageInfo | null {
  if (!chatId) return null;
  for (let i = updates.length - 1; i >= 0; i--) {
    const update = updates[i];
    if (update.updateType !== ChatUpdateType.MESSAGE) continue;
    let payload: Record<string, unknown>;
    try {
      const data = JSON.parse(update.dataJson || "{}") as Record<string, unknown>;
      payload =
        typeof data.message === "object" && data.message !== null
          ? (data.message as Record<string, unknown>)
          : data;
    } catch {
      continue;
    }
    if (payload.thread !== chatId) continue;
    const tokens = payload.thread_token_count;
    const threshold = payload.compaction_threshold;
    if (typeof tokens !== "number" || typeof threshold !== "number") continue;
    return { threadTokenCount: tokens, compactionThreshold: threshold };
  }
  return null;
}

// ============================================================================
// Chat Streaming Service (gRPC)
// ============================================================================

export class ChatStreamingService extends ReconnectingStream<ChatStreamEvent> {
  private callbacks: ChatStreamCallbacks;
  private chatId: string | undefined = undefined;
  private lastChatSequence: bigint = 0n;
  // The project the user stream is scoped to. The server forwards, on this
  // stream, the chat's user-level updates that scope filters out.
  private projectId: string | undefined = undefined;
  private lastChatGapResyncAt = 0;
  // The chat whose initial sync has not arrived yet for the current
  // subscription, or null. Set when a subscription to a chat begins; cleared
  // by settleChatSync when the server sends caught_up for it. Survives
  // reconnects in between, but a reconnect of an already-synced subscription
  // (gap resync, network blip) does not re-open it.
  private chatSyncPendingFor: string | null = null;
  // Chats the server refused to stream (NotFound: deleted, or not this
  // user's). Remembered so the self-healing reconcile, which re-asserts the
  // rendered chat on every connection change, cannot turn one refusal into a
  // reconnect loop. Per service instance: a fresh service tries once more.
  private rejectedChatIds = new Set<string>();

  constructor(callbacks: ChatStreamCallbacks, projectId?: string) {
    super(LOG_PREFIX_STREAM, {
      onStatusChange: (status) => callbacks.onStatusChange?.(status),
      onError: (error) => callbacks.onError?.(error),
    });
    this.callbacks = callbacks;
    this.projectId = projectId;
    // Idle until the first subscribeToChat: there is nothing to stream yet.
    this.isIntentionallyClosed = true;
  }

  /**
   * Stream a chat's detail events.
   *
   * Callers (`reconcileChatSubscription`'s self-healing invariant, in
   * particular) invoke this on every render of the active chat, and cannot
   * tell "genuinely need to (re)subscribe" from "already subscribed, just
   * confirming" without asking this service — so this method has to make
   * that distinction itself, explicitly, rather than always reconnecting:
   *
   *  - Different chatId (a real chat switch): always reconnect. The chat
   *    being left hands its cursor back via `onChatCursorRelease`, and the
   *    chat being entered resumes from `resolveChatResumeSequence` — the
   *    owner of its cached state answers with the sequence that state is
   *    current as of, or 0 when it holds none it can vouch for. A cursor
   *    asks the server for a replay of (cursor, latest]; 0 asks for a
   *    snapshot. The server may answer a cursor with a snapshot instead
   *    (gap too large, cursor ahead of the chat), which replaces state as
   *    it always has, so either reply is handled.
   *  - Same chatId, already connected to it: true no-op. Nothing changed.
   *  - Same chatId, but not yet confirmed connected (a connection attempt
   *    for this exact chat is already in flight — e.g. triggered a moment
   *    ago by a gap-resync): also a no-op. Firing a second reconnect here
   *    would supersede the in-flight one before it completes (wasted round
   *    trip, a logged stream error) AND would zero `lastChatSequence` and
   *    force a full snapshot even though the in-flight connection was about
   *    to deliver an incremental replay.
   *  - Same chatId, genuinely disconnected (no attempt in flight): reconnect
   *    IS needed to restore the subscription, but the cursor is preserved —
   *    it's the same chat, so the server can still replay
   *    (since_seq, latest] instead of sending a snapshot.
   *
   * Only this stream reconnects; the user stream is never touched.
   */
  subscribeToChat(chatId: string): void {
    if (this.rejectedChatIds.has(chatId)) {
      logger.debug(
        `${LOG_PREFIX_STREAM} Not subscribing to a chat the server refused`,
        { chatId: chatId.slice(0, 8) },
      );
      return;
    }
    if (this.chatId === chatId) {
      if (this.isConnected_ || this.connectAttemptInFlight) {
        logger.debug(
          `${LOG_PREFIX_STREAM} Already subscribed (or subscribing) to this chat — ignoring redundant (re)assert`,
          { chatId: chatId.slice(0, 8), connected: this.isConnected_ },
        );
        return;
      }
      // Same chat, but disconnected with nothing in flight — reconnect to
      // restore the subscription, preserving the cursor for a replay.
      logger.info(
        `${LOG_PREFIX_STREAM} Reasserting chat subscription after disconnect`,
        { chatId: chatId.slice(0, 8) },
      );
      this.reconnect();
      return;
    }

    logger.info(`${LOG_PREFIX_STREAM} Subscribing to chat details`, {
      chatId: chatId.slice(0, 8),
      previousChatId: this.chatId?.slice(0, 8),
    });

    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("grpc-stream-subscribe-chat", { chatId });
    }

    this.releaseChatCursor();
    const resumeFrom = this.callbacks.resolveChatResumeSequence?.(chatId) ?? 0n;
    this.chatId = chatId;
    this.lastChatSequence = resumeFrom > 0n ? resumeFrom : 0n;
    this.setChatSyncPendingFor(chatId);
    if (this.lastChatSequence > 0n) {
      logger.info(`${LOG_PREFIX_STREAM} Resuming chat from cached cursor`, {
        chatId: chatId.slice(0, 8),
        chatSinceSeq: Number(this.lastChatSequence),
      });
    }

    this.reconnect();
  }

  /**
   * Stop streaming the current chat. The connection goes idle until the next
   * subscribeToChat.
   */
  unsubscribe(): void {
    if (!this.chatId) return;

    logger.info(`${LOG_PREFIX_STREAM} Unsubscribing from chat details`, {
      chatId: this.chatId.slice(0, 8),
    });

    this.releaseChatCursor();
    this.chatId = undefined;
    this.lastChatSequence = 0n;
    this.setChatSyncPendingFor(null);
    this.close(false);
  }

  /**
   * The user stream's project scope changed. The server picks the chat's
   * cross-project user updates by it, so an open chat stream reconnects —
   * from its cursor, a replay of nothing when the chat did not change.
   */
  setProjectScope(projectId: string | undefined): void {
    if (this.projectId === projectId) return;
    this.projectId = projectId;
    if (this.chatId && !this.isIntentionallyClosed) {
      this.reconnectNow("resubscribe");
    }
  }

  /** Tear the stream down for good (the shell disconnected). */
  stop(): void {
    logger.info(`${LOG_PREFIX_STREAM} Stopping`);
    this.releaseChatCursor();
    this.setChatSyncPendingFor(null);
    this.close(true);
  }

  getChatId(): string | undefined {
    return this.chatId;
  }

  getLastChatSequence(): number {
    return Number(this.lastChatSequence);
  }

  /** (Re)open the stream for the current chat, keeping its cursor. */
  private reconnect(): void {
    if (this.isIntentionallyClosed) {
      this.open();
      return;
    }
    this.reconnectNow("resubscribe");
  }

  /**
   * Hand the cursor of the chat being left back to whoever owns that chat's
   * cached state. Every exit from a chat — switch, unsubscribe, stop — goes
   * through here, so the owner always holds the cursor matching its state.
   */
  private releaseChatCursor(): void {
    if (!this.chatId) return;
    this.callbacks.onChatCursorRelease?.(this.chatId, this.lastChatSequence);
  }

  /**
   * Forget a chat the server will not stream to this user. No cursor is
   * handed back: there is no state left on the server for one to describe.
   */
  private dropRefusedChat(chatId: string): void {
    logger.warn(
      `${LOG_PREFIX_STREAM} Server refused the chat subscription — continuing without it`,
      { chatId: chatId.slice(0, 8) },
    );
    this.rejectedChatIds.add(chatId);
    this.chatId = undefined;
    this.lastChatSequence = 0n;
    this.setChatSyncPendingFor(null);
    this.close(false);
    this.callbacks.onChatSubscriptionRejected?.(chatId);
  }

  private setChatSyncPendingFor(chatId: string | null): void {
    if (this.chatSyncPendingFor === chatId) return;
    this.chatSyncPendingFor = chatId;
    this.callbacks.onChatSyncPending?.(chatId);
  }

  /**
   * The current subscription's initial sync is complete. Called only from
   * the caught_up case: the server sends that exactly once per stream, after
   * the snapshot or the replay — including a replay of nothing, which sends
   * no chat frame at all. No other frame can stand in for it: a snapshot may
   * still be followed by nothing else, but a replay batch's latest_sequence
   * is that batch's own max, so "reaching" it says nothing about whether more
   * batches follow; and a heartbeat proves only that the socket is alive.
   */
  private settleChatSync(latestSequence: bigint): void {
    if (this.chatSyncPendingFor === null) return;
    logger.info(`${LOG_PREFIX_STREAM} Chat sync complete`, {
      chatId: this.chatSyncPendingFor.slice(0, 8),
      serverSeq: Number(latestSequence),
      chatSeq: Number(this.lastChatSequence),
    });
    this.setChatSyncPendingFor(null);
  }

  /**
   * A persisted chat update was missed upstream (sequence gap). Reconnect
   * preserving the cursor; the server replays (since_seq, latest] from the
   * DB, restoring the missed updates in order.
   */
  private resyncAfterGap(gapSeq: bigint): void {
    const now = Date.now();
    if (now - this.lastChatGapResyncAt < CHAT_GAP_RESYNC_MIN_INTERVAL_MS) {
      // A resync is already in flight; its replay covers this gap too.
      return;
    }
    this.lastChatGapResyncAt = now;

    logger.warn(
      `${LOG_PREFIX_STREAM} Chat update sequence gap detected — resyncing from last contiguous sequence`,
      {
        chatId: this.chatId?.slice(0, 8),
        lastContiguous: Number(this.lastChatSequence),
        gapSeq: Number(gapSeq),
      },
    );
    this.reconnectNow("resubscribe");
  }

  protected openStream(signal: AbortSignal): AsyncIterable<ChatStreamEvent> {
    const client = connectClient(StreamingService);
    const request = create(StreamChatUpdatesRequestSchema, {
      chatId: this.chatId ?? "",
      sinceSeq: this.lastChatSequence,
      projectId: this.projectId,
    });
    logger.info(`${LOG_PREFIX_STREAM} Connecting to chat stream`, {
      chatId: this.chatId?.slice(0, 8),
      chatSinceSeq: Number(this.lastChatSequence),
    });
    return client.streamChatUpdates(request, { signal });
  }

  protected describe(): Record<string, unknown> {
    return {
      chatId: this.chatId?.slice(0, 8),
      chatFromSeq: Number(this.lastChatSequence),
    };
  }

  /**
   * The server refuses a chat that is gone (deleted, possibly from another
   * device) or is not this user's. Every retry would carry the same chat and
   * be refused the same way, so drop it and go idle instead of backing off
   * against it forever.
   */
  protected handleConnectFailure(error: unknown, delivered: boolean): boolean {
    if (!delivered && this.chatId && isChatRefused(error)) {
      this.dropRefusedChat(this.chatId);
      return true;
    }
    return false;
  }

  protected handleEvent(event: ChatStreamEvent): void {
    const chatId = this.chatId;
    if (!chatId) return;

    switch (event.event.case) {
      case "snapshot": {
        const snapshot = event.event.value;
        if (tabSwitchProfiler.isEnabled()) {
          tabSwitchProfiler.mark("grpc-stream-sync-snapshot-received", {
            chatId,
            messageCount: snapshot.messages.length,
            otherUpdates: snapshot.otherUpdates.length,
          });
        }
        logger.info(`${LOG_PREFIX_STREAM} Received chat snapshot`, {
          chatId: chatId.slice(0, 8),
          messageCount: snapshot.messages.length,
          otherUpdates: snapshot.otherUpdates.length,
          latestSequence: Number(snapshot.latestSequence),
        });

        this.lastChatSequence = snapshot.latestSequence;

        // Wrap proto Messages with update_type discriminator
        const messageUpdates: ChatUpdate[] = snapshot.messages.map(
          (msg): ProtoMessageUpdate => ({
            update_type: "message",
            message: msg,
          }),
        );

        // Convert other updates (approvals, threads, etc.)
        const otherUpdates: ChatUpdate[] = snapshot.otherUpdates
          .map(convertChatUpdateData)
          .filter((u): u is ChatUpdate => u !== null);

        // A snapshot replaces the chat's state (it prevents cross-chat
        // message leaking and drops what the server no longer has).
        this.callbacks.onChatSnapshot(chatId, [...messageUpdates, ...otherUpdates]);

        this.callbacks.onChatPaginationInfo?.(chatId, {
          total: snapshot.total,
          hasMore: snapshot.hasMore,
          oldestSeq: Number(snapshot.oldestSeq),
        });

        this.callbacks.onChatContextUsage?.(chatId, {
          threadTokenCount: Number(snapshot.threadTokenCount),
          compactionThreshold: Number(snapshot.compactionThreshold),
        });
        break;
      }

      case "updates": {
        const batch = event.event.value;

        // Persisted chat updates carry contiguous per-chat sequence numbers
        // (ephemeral streaming deltas carry 0). A jump past cursor+1 means an
        // upstream drop (e.g. NATS hub slow consumer): apply the contiguous
        // prefix, drop the rest, and resync — the reconnect resumes from
        // lastChatSequence and the server replays the missed range from the
        // DB in order. batch.latestSequence is deliberately NOT trusted for
        // the cursor: it echoes server state that can be ahead of what this
        // client actually received.
        const accepted: ChatUpdateData[] = [];
        let gapAt: bigint | null = null;
        for (const update of batch.updates) {
          const seq = update.sequenceNumber;
          if (seq === 0n) {
            accepted.push(update); // ephemeral delta — no ordering contract
            continue;
          }
          if (this.lastChatSequence > 0n && seq > this.lastChatSequence + 1n) {
            gapAt = seq;
            break;
          }
          if (seq > this.lastChatSequence) {
            this.lastChatSequence = seq;
          }
          accepted.push(update);
        }

        const updates: ChatUpdate[] = accepted
          .map(convertChatUpdateData)
          .filter((u): u is ChatUpdate => u !== null);

        if (updates.length > 0) {
          this.callbacks.onChatUpdate(chatId, updates);
        }

        // A replay resume gets no snapshot, so the compaction indicator would
        // keep showing the usage from when the chat was last open. Every
        // persisted main-thread message carries the current usage; the
        // latest one accepted here is what a snapshot would report now.
        const contextUsage = latestMainThreadContextUsage(accepted, chatId);
        if (contextUsage) {
          this.callbacks.onChatContextUsage?.(chatId, contextUsage);
        }

        if (gapAt !== null) {
          this.resyncAfterGap(gapAt);
        }
        break;
      }

      case "caughtUp": {
        // The initial chat sync for this stream is over — the only signal
        // that clears the syncing indicator. See settleChatSync.
        this.settleChatSync(event.event.value.latestSequence);
        break;
      }

      case "userUpdates": {
        const updates = event.event.value.updates.map(convertUserUpdateData);
        if (updates.length > 0) {
          this.callbacks.onUserUpdates?.(updates);
        }
        break;
      }

      case "heartbeat": {
        // Liveness only (lastEventAt is bumped for every event).
        break;
      }

      default:
        logger.warn(`${LOG_PREFIX_STREAM} Unknown event type`, {
          case: event.event.case,
        });
    }
  }
}
