// Copyright (c) 2025 Reliant Labs
// Unified gRPC streaming client for user and chat updates
// A single stream handles BOTH global user-level events and per-chat detail events.

import { createClient } from "@connectrpc/connect";
import { create, fromJsonString } from "@bufbuild/protobuf";
import { getTransport } from "./grpc-client";
import { logger } from "../lib/logger";
import { tabSwitchProfiler } from "../lib/tabSwitchProfiler";
import {
  StreamingService,
  StreamUserUpdatesRequestSchema,
  ChatUpdateType,
  UserUpdateType,
  EntityType,
  NodeExecutionEventType,
  WorkflowExecutionEventType,
  NodeExecutionStatus,
  WorkflowExecutionStatus,
  ExecutionLogLevel,
  type UserStreamEvent,
  type ChatSyncSnapshot,
  type ChatUpdateBatch,
  type UserSyncInfo,
  type UserUpdateBatch,
  type ChatUpdateData,
  type UserUpdateData,
  type NodeExecutionEvent,
  type WorkflowExecutionEvent,
  type ExecutionLog,
} from "../gen/reliant/v1/streaming_pb";
import {
  MessageSchema,
  type Message as ProtoMessage,
} from "../gen/reliant/v1/chat_pb";

// Re-export proto enums for consumers
export {
  ChatUpdateType,
  UserUpdateType,
  EntityType,
  NodeExecutionEventType,
  WorkflowExecutionEventType,
  NodeExecutionStatus,
  WorkflowExecutionStatus,
  ExecutionLogLevel,
};

// Re-export all streaming types from the types module
export type {
  ToolApprovalUpdate,
  ActiveThreadUpdate,
  WorkflowStatusUpdate,
  ToolCallUpdate,
  ErrorUpdate,
  InfoUpdate,
  ChatMetadataUpdate,
  StreamingDelta,
  RunOutputUpdate,
  RefetchUpdate,
  NodeExecutionUpdate,
  WorkflowExecutionUpdate,
  ExecutionLogUpdate,
  ProtoMessageUpdate,
  QuestionUpdate,
  ChatUpdate,
  ConnectionStatus,
  MessagePaginationInfo,
  ContextUsageInfo,
  UserUpdate,
  GlobalWebSocketCallbacks,
} from "../types/streaming";

import type {
  ChatUpdate,
  ProtoMessageUpdate,
  NodeExecutionUpdate,
  WorkflowExecutionUpdate,
  ExecutionLogUpdate,
  UserUpdate,
  GlobalWebSocketCallbacks,
} from "../types/streaming";

// Log prefix for filtering/debugging
const LOG_PREFIX_STREAM = "[📡 gRPCStream]";

// Gap-resync pacing. Chat gaps are exact (per-chat sequences are contiguous),
// so the interval only guards against reconnect loops. User-update jumps can
// be legitimate (the server filters live events by project), so their resync
// is throttled harder.
const CHAT_GAP_RESYNC_MIN_INTERVAL_MS = 1_000;
const USER_GAP_RESYNC_THROTTLE_MS = 5_000;

// Reconnect pacing. This stream is the *only* push path for the whole UI, so
// there is no state in which giving up permanently is correct — a stream that
// stops retrying leaves the app silently stale until a full page reload. We
// retry forever with exponential backoff capped at MAX_RECONNECT_DELAY_MS, and
// the wake triggers (online / tab-visible) collapse the backoff to zero the
// moment the user is plausibly back.
const BASE_RECONNECT_DELAY_MS = 1_000;
const MAX_RECONNECT_DELAY_MS = 30_000;
// Cap the exponent so Math.pow stays finite over a long outage.
const MAX_RECONNECT_EXPONENT = 6;

// Liveness watchdog. The server sends a heartbeat every 30s (see
// internal/grpc/services/streaming.go heartbeatInterval). A half-open
// connection — backend SIGKILLed under `air`, laptop slept, proxy dropped the
// socket without a FIN — never surfaces as a stream error, so `for await` just
// blocks forever and no reconnect is attempted. Treat "no event of any kind
// for 2.5 heartbeat intervals" as dead and force a reconnect.
const STREAM_STALE_TIMEOUT_MS = 75_000;
const WATCHDOG_TICK_MS = 15_000;

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
    default:
      return true;
  }
}

/**
 * Convert gRPC ChatUpdateData to ChatUpdate format.
 * ChatUpdateData uses a dataJson field containing the actual update data.
 * The JSON data uses snake_case because it comes from Go's encoding/json.
 */
function convertChatUpdateData(update: ChatUpdateData): ChatUpdate | null {
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
 * Convert gRPC UserUpdateData to UserUpdate format
 */
function convertUserUpdateData(update: UserUpdateData): UserUpdate {
  let data: Record<string, unknown> = {};
  try {
    data = update.dataJson ? JSON.parse(update.dataJson) : {};
  } catch (error) {
    logger.error(`${LOG_PREFIX_STREAM} Failed to parse update data`, {
      updateType: update.updateType,
      error,
    });
  }

  return {
    id: update.id,
    user_id: update.userId,
    sequence_number: Number(update.sequenceNumber),
    project_id: update.projectId,
    worktree_id: update.worktreeId,
    chat_id: update.chatId,
    update_type: update.updateType,
    entity_type: update.entityType,
    entity_id: update.entityId,
    data,
    created_at: update.createdAt,
  };
}

// ============================================================================
// Workflow Execution Event Converters
// ============================================================================

function convertNodeExecutionEvent(
  event: NodeExecutionEvent,
  chatId: string,
): NodeExecutionUpdate {
  const node = event.node;
  return {
    update_type: "node_execution",
    event_type: event.eventType,
    node_id: node?.nodeId || "",
    node_type: node?.nodeType || "",
    status: node?.status ?? NodeExecutionStatus.PENDING,
    workflow_id: node?.workflowId || "",
    chat_id: chatId,
    parent_node_id: node?.parentNodeId,
    activity_id: node?.activityId,
    started_at:
      node?.startedAt !== undefined ? Number(node.startedAt) : undefined,
    completed_at:
      node?.completedAt !== undefined ? Number(node.completedAt) : undefined,
    duration_ms:
      node?.durationMs !== undefined ? Number(node.durationMs) : undefined,
    exit_code: node?.exitCode,
    error_message: node?.errorMessage,
    iteration: node?.iteration,
    max_iterations: node?.maxIterations,
    progress_message: event.progressMessage,
    progress_percent: event.progressPercent,
    metadata: node?.metadata,
  };
}

function convertWorkflowExecutionEvent(
  event: WorkflowExecutionEvent,
  chatId: string,
): WorkflowExecutionUpdate {
  const workflow = event.workflow;
  return {
    update_type: "workflow_execution",
    event_type: event.eventType,
    workflow_id: workflow?.workflowId || "",
    workflow_name: workflow?.workflowName || "",
    chat_id: chatId,
    status: workflow?.status ?? WorkflowExecutionStatus.RUNNING,
    parent_workflow_id: workflow?.parentWorkflowId,
    thread: workflow?.thread,
    active_nodes: workflow?.activeNodes,
    started_at:
      workflow?.startedAt !== undefined
        ? Number(workflow.startedAt)
        : undefined,
    completed_at:
      workflow?.completedAt !== undefined
        ? Number(workflow.completedAt)
        : undefined,
    timestamp: Date.now(),
  };
}

function convertExecutionLog(
  log: ExecutionLog,
  chatId: string,
): ExecutionLogUpdate {
  return {
    update_type: "execution_log",
    id: log.id,
    workflow_id: log.workflowId,
    chat_id: chatId,
    node_id: log.nodeId,
    level: log.level,
    message: log.message,
    timestamp: Number(log.timestamp),
    source: log.source,
    fields: log.fields,
  };
}

// ============================================================================
// Unified Streaming Service (gRPC)
// Handles BOTH global user-level events AND per-chat detail events
// through a single gRPC server-streaming connection.
// ============================================================================

export class UserStreamingService {
  private callbacks: GlobalWebSocketCallbacks;
  private abortController: AbortController | null = null;
  private projectId: string | undefined = undefined;
  private isIntentionallyClosed = false;
  private reconnectAttempts = 0;
  private lastSequence: bigint = 0n;
  private lastChatSequence: bigint = 0n;
  private isConnected_ = false;
  private subscribedChatId: string | undefined = undefined;
  private lastChatGapResyncAt = 0;
  private lastUserGapResyncAt = 0;
  // Liveness / lifecycle bookkeeping. `connectAttemptInFlight` and
  // `reconnectTimer` exist so start() can tell "a connection is genuinely being
  // worked on" from "we hold a stale AbortController that will never fire".
  private connectAttemptInFlight = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private watchdogTimer: ReturnType<typeof setInterval> | null = null;
  private lastEventAt = 0;
  private wakeHandlersBound = false;

  constructor(callbacks: GlobalWebSocketCallbacks) {
    this.callbacks = callbacks;
  }

  // --- Wake triggers -------------------------------------------------------
  // Backoff can be sitting at 30s when the user comes back to a slept laptop or
  // a reconnected network. Both signals mean "conditions just changed" — drop
  // the backoff and retry immediately instead of making the user wait out a
  // timer (or reload the page).
  private handleOnline = (): void => {
    if (this.isIntentionallyClosed || this.isConnected_) return;
    logger.info(`${LOG_PREFIX_STREAM} Network back online — reconnecting now`);
    this.reconnectNow("online");
  };

  private handleVisibilityChange = (): void => {
    if (document.visibilityState !== "visible") return;
    if (this.isIntentionallyClosed || this.isConnected_) return;
    logger.info(`${LOG_PREFIX_STREAM} Tab visible again — reconnecting now`);
    this.reconnectNow("visible");
  };

  private bindWakeHandlers(): void {
    if (this.wakeHandlersBound || typeof window === "undefined") return;
    window.addEventListener("online", this.handleOnline);
    document.addEventListener("visibilitychange", this.handleVisibilityChange);
    this.wakeHandlersBound = true;
  }

  private unbindWakeHandlers(): void {
    if (!this.wakeHandlersBound || typeof window === "undefined") return;
    window.removeEventListener("online", this.handleOnline);
    document.removeEventListener(
      "visibilitychange",
      this.handleVisibilityChange,
    );
    this.wakeHandlersBound = false;
  }

  // --- Liveness watchdog ---------------------------------------------------
  private armWatchdog(): void {
    this.lastEventAt = Date.now();
    if (this.watchdogTimer !== null) return;
    this.watchdogTimer = setInterval(() => {
      if (!this.isConnected_ || this.isIntentionallyClosed) return;
      const silentFor = Date.now() - this.lastEventAt;
      if (silentFor < STREAM_STALE_TIMEOUT_MS) return;

      logger.warn(
        `${LOG_PREFIX_STREAM} No events (not even heartbeats) — connection is half-open, forcing reconnect`,
        { silentForMs: silentFor, staleAfterMs: STREAM_STALE_TIMEOUT_MS },
      );
      // The socket is dead but `for await` will never unblock on its own.
      // reconnectNow() aborts it, which makes the orphaned loop's catch see a
      // superseded controller and bail out quietly.
      this.isConnected_ = false;
      this.callbacks.onStatusChange("disconnected");
      this.reconnectNow("stale");
    }, WATCHDOG_TICK_MS);
  }

  private disarmWatchdog(): void {
    if (this.watchdogTimer !== null) {
      clearInterval(this.watchdogTimer);
      this.watchdogTimer = null;
    }
  }

  /**
   * Start the unified stream.
   * @param fromSeq User-level sequence to resume from
   * @param subscribeChatId Optional chat ID to subscribe for detail events
   * @param chatFromSeq Chat-level sequence to resume from (0 for snapshot)
   */
  start(
    fromSeq: number = 0,
    subscribeChatId?: string,
    chatFromSeq: number = 0,
    projectId?: string,
  ): void {
    // Guard on work actually in flight, NOT on the AbortController. A failed
    // attempt leaves behind a non-aborted controller, so the old check made a
    // dead service permanently un-restartable — start() would no-op forever.
    if (
      this.isConnected_ ||
      this.connectAttemptInFlight ||
      this.reconnectTimer !== null
    ) {
      logger.warn(`${LOG_PREFIX_STREAM} Already connected or connecting`);
      return;
    }

    logger.info(`${LOG_PREFIX_STREAM} Starting unified gRPC stream`, {
      fromSeq,
      subscribeChatId: subscribeChatId?.slice(0, 8),
      chatFromSeq,
    });

    this.isIntentionallyClosed = false;
    this.lastSequence = BigInt(fromSeq);
    this.lastChatSequence = BigInt(chatFromSeq);
    this.subscribedChatId = subscribeChatId;
    this.projectId = projectId;
    this.reconnectAttempts = 0;
    this.bindWakeHandlers();

    void this.establishConnection();
  }

  /**
   * Subscribe to a chat's detail events. This disconnects the current stream
   * and reconnects with the new chat subscription.
   */
  subscribeToChatDetails(chatId: string): void {
    logger.info(`${LOG_PREFIX_STREAM} Subscribing to chat details`, {
      chatId: chatId.slice(0, 8),
    });

    if (tabSwitchProfiler.isEnabled()) {
      tabSwitchProfiler.mark("grpc-stream-subscribe-chat", { chatId });
    }

    this.subscribedChatId = chatId;
    this.lastChatSequence = 0n; // Always request full snapshot on new subscription

    // Reconnect with the new subscription
    this.reconnectWithNewSubscription();
  }

  /**
   * Unsubscribe from chat detail events. Reconnects without chat subscription.
   */
  unsubscribeFromChatDetails(): void {
    if (!this.subscribedChatId) return;

    logger.info(`${LOG_PREFIX_STREAM} Unsubscribing from chat details`, {
      chatId: this.subscribedChatId.slice(0, 8),
    });

    this.subscribedChatId = undefined;
    this.lastChatSequence = 0n;

    // Reconnect without chat subscription
    this.reconnectWithNewSubscription();
  }

  /**
   * A persisted chat update was missed upstream (sequence gap). Reconnect
   * preserving both cursors; the server replays (chat_since_seq, latest]
   * from the DB, restoring the missed updates in order.
   */
  private resyncChatStreamAfterGap(gapSeq: bigint): void {
    const now = Date.now();
    if (now - this.lastChatGapResyncAt < CHAT_GAP_RESYNC_MIN_INTERVAL_MS) {
      // A resync is already in flight; its replay covers this gap too.
      return;
    }
    this.lastChatGapResyncAt = now;

    logger.warn(
      `${LOG_PREFIX_STREAM} Chat update sequence gap detected — resyncing from last contiguous sequence`,
      {
        chatId: this.subscribedChatId?.slice(0, 8),
        lastContiguous: Number(this.lastChatSequence),
        gapSeq: Number(gapSeq),
      },
    );
    this.reconnectWithNewSubscription();
  }

  /**
   * The user-update sequence jumped. Rewind the resume cursor to the last
   * pre-jump sequence and reconnect: the server replays the range from the
   * DB (project-filtered), filling any genuinely dropped events. Throttled,
   * because jumps can also be caused by the server's project filter.
   */
  private scheduleUserGapResync(fromSeq: bigint): void {
    const now = Date.now();
    if (now - this.lastUserGapResyncAt < USER_GAP_RESYNC_THROTTLE_MS) {
      logger.debug(
        `${LOG_PREFIX_STREAM} User update sequence jump — resync throttled`,
        { fromSeq: Number(fromSeq) },
      );
      return;
    }
    this.lastUserGapResyncAt = now;

    logger.warn(
      `${LOG_PREFIX_STREAM} User update sequence jump detected — resyncing`,
      {
        fromSeq: Number(fromSeq),
        currentSeq: Number(this.lastSequence),
      },
    );
    this.lastSequence = fromSeq;
    this.reconnectWithNewSubscription();
  }

  /**
   * Reconnect the stream, preserving user-level sequence but resetting chat state.
   */
  private reconnectWithNewSubscription(): void {
    this.reconnectNow("resubscribe");
  }

  /**
   * Tear down the current connection (if any) and reconnect immediately,
   * resetting the backoff. Preserves both resume cursors, so the server
   * replays (since_seq, latest] from the DB and nothing is lost.
   */
  private reconnectNow(reason: string): void {
    if (this.isIntentionallyClosed) return;

    // Cancel a pending backoff timer so we don't end up with two connections.
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }

    // Abort the old connection. The old stream's catch block will detect
    // that this.abortController has been replaced (by establishConnection)
    // and bail out instead of calling attemptReconnect().
    if (this.abortController) {
      this.abortController.abort();
    }
    this.isConnected_ = false;
    this.disarmWatchdog();
    this.reconnectAttempts = 0;
    logger.info(`${LOG_PREFIX_STREAM} Immediate reconnect`, { reason });

    // Establish new connection (creates a new AbortController)
    void this.establishConnection();
  }

  private async establishConnection(): Promise<void> {
    this.callbacks.onStatusChange("connecting");
    this.connectAttemptInFlight = true;
    this.abortController = new AbortController();

    // Capture this connection's abort controller so we can detect if it gets
    // replaced by reconnectWithNewSubscription() before our catch block runs.
    const myAbortController = this.abortController;

    try {
      const client = createClient(StreamingService, getTransport());

      const request = create(StreamUserUpdatesRequestSchema, {
        sinceSeq: this.lastSequence,
        subscribeChatId: this.subscribedChatId,
        chatSinceSeq: this.lastChatSequence,
        projectId: this.projectId,
      });

      logger.info(`${LOG_PREFIX_STREAM} Connecting to unified gRPC stream`, {
        sinceSeq: Number(this.lastSequence),
        subscribeChatId: this.subscribedChatId?.slice(0, 8),
        chatSinceSeq: Number(this.lastChatSequence),
      });

      for await (const event of client.streamUserUpdates(request, {
        signal: myAbortController.signal,
      })) {
        // If our controller was replaced, a new connection took over — bail out.
        if (this.abortController !== myAbortController) {
          logger.info(`${LOG_PREFIX_STREAM} Connection superseded, stopping old stream`);
          return;
        }

        // Any event — including a bare heartbeat — proves the socket is alive.
        this.lastEventAt = Date.now();

        if (!this.isConnected_) {
          this.isConnected_ = true;
          this.connectAttemptInFlight = false;
          this.callbacks.onStatusChange("connected");
          this.reconnectAttempts = 0;
          this.armWatchdog();
        }

        this.handleEvent(event);
      }

      // If our controller was replaced, a new connection took over — don't reconnect.
      if (this.abortController !== myAbortController) {
        logger.info(`${LOG_PREFIX_STREAM} Connection superseded after stream end`);
        return;
      }

      logger.info(`${LOG_PREFIX_STREAM} Stream ended normally`);
      this.isConnected_ = false;
      this.connectAttemptInFlight = false;
      this.disarmWatchdog();
      this.callbacks.onStatusChange("disconnected");

      if (!this.isIntentionallyClosed) {
        this.attemptReconnect();
      }
    } catch (error) {
      // If our controller was replaced, a new connection took over — don't reconnect.
      if (this.abortController !== myAbortController) {
        logger.info(`${LOG_PREFIX_STREAM} Connection superseded (caught error from old stream)`);
        return;
      }

      this.isConnected_ = false;
      this.connectAttemptInFlight = false;
      this.disarmWatchdog();

      if (this.isIntentionallyClosed || myAbortController.signal.aborted) {
        logger.info(`${LOG_PREFIX_STREAM} Stream aborted (intentional)`);
        this.callbacks.onStatusChange("disconnected");
        return;
      }

      const errorMessage =
        error instanceof Error ? error.message : String(error);
      logger.error(`${LOG_PREFIX_STREAM} Stream error`, {
        error: errorMessage,
      });
      this.callbacks.onError(errorMessage);
      this.callbacks.onStatusChange("error");

      if (!this.isIntentionallyClosed) {
        this.attemptReconnect();
      }
    }
  }

  private handleEvent(event: UserStreamEvent): void {
    switch (event.event.case) {
      // --- User-level events ---
      case "sync": {
        const syncInfo = event.event.value;
        logger.info(`${LOG_PREFIX_STREAM} Received sync info`, {
          lastSequence: Number(syncInfo.lastSequence),
        });
        // Deliberately does NOT advance this.lastSequence: the server sends
        // sync BEFORE replaying (since_seq, latest]. The resume cursor must
        // only reflect updates we actually processed, otherwise a disconnect
        // mid-replay would skip the remaining updates forever on reconnect.
        this.callbacks.onSync(Number(syncInfo.lastSequence));
        break;
      }

      case "updates": {
        const batch = event.event.value;
        // Process events BEFORE advancing the sequence number.
        // If the stream is aborted mid-batch (e.g. chat-switch reconnect),
        // the un-acked sequence ensures the server re-sends on reconnect.
        const preBatchCursor = this.lastSequence;
        const wsUpdates = batch.updates.map(convertUserUpdateData);
        this.callbacks.onUpdate(wsUpdates);
        let jumped = false;
        let cursor = preBatchCursor;
        for (const update of batch.updates) {
          if (cursor > 0n && update.sequenceNumber > cursor + 1n) {
            jumped = true;
          }
          if (update.sequenceNumber > cursor) {
            cursor = update.sequenceNumber;
          }
        }
        this.lastSequence = cursor;
        if (jumped) {
          // User sequences are per-user but the server filters live events by
          // project, so a jump is only *possibly* a dropped event. The batch
          // has been applied; a throttled replay-resync from the pre-jump
          // cursor fills anything that was really dropped (re-delivery of
          // already-applied updates is idempotent upsert/patching).
          this.scheduleUserGapResync(preBatchCursor);
        }
        break;
      }

      case "heartbeat": {
        break;
      }

      // --- Per-chat detail events ---
      case "chatSyncSnapshot": {
        const snapshot = event.event.value;
        if (tabSwitchProfiler.isEnabled()) {
          tabSwitchProfiler.mark("grpc-stream-sync-snapshot-received", {
            chatId: this.subscribedChatId,
            messageCount: snapshot.messages.length,
            otherUpdates: snapshot.otherUpdates.length,
          });
        }
        logger.info(`${LOG_PREFIX_STREAM} Received chat snapshot`, {
          chatId: this.subscribedChatId?.slice(0, 8),
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

        // Route snapshot through onChatSnapshot (replace semantics) if available,
        // otherwise fall back to onChatUpdate (merge semantics)
        const allUpdates = [...messageUpdates, ...otherUpdates];
        if (this.callbacks.onChatSnapshot) {
          this.callbacks.onChatSnapshot(allUpdates);
        } else {
          this.callbacks.onChatUpdate?.(allUpdates);
        }

        if (this.callbacks.onChatPaginationInfo) {
          this.callbacks.onChatPaginationInfo({
            total: snapshot.total,
            hasMore: snapshot.hasMore,
            oldestSeq: Number(snapshot.oldestSeq),
          });
        }

        if (this.callbacks.onChatContextUsage) {
          this.callbacks.onChatContextUsage({
            threadTokenCount: Number(snapshot.threadTokenCount),
            compactionThreshold: Number(snapshot.compactionThreshold),
          });
        }
        break;
      }

      case "chatUpdates": {
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
          this.callbacks.onChatUpdate?.(updates);
        }

        if (gapAt !== null) {
          this.resyncChatStreamAfterGap(gapAt);
        }
        break;
      }

      case "workflowEvent": {
        const workflowEvent = event.event.value;
        const chatId = this.subscribedChatId || "";
        const workflowUpdate = convertWorkflowExecutionEvent(
          workflowEvent,
          chatId,
        );
        this.callbacks.onChatUpdate?.([workflowUpdate]);
        break;
      }

      case "nodeEvent": {
        const nodeEvent = event.event.value;
        const chatId = this.subscribedChatId || "";
        const nodeUpdate = convertNodeExecutionEvent(nodeEvent, chatId);
        this.callbacks.onChatUpdate?.([nodeUpdate]);
        break;
      }

      case "executionLog": {
        const logEntry = event.event.value;
        const chatId = this.subscribedChatId || "";
        const logUpdate = convertExecutionLog(logEntry, chatId);
        this.callbacks.onChatUpdate?.([logUpdate]);
        break;
      }

      default:
        logger.warn(`${LOG_PREFIX_STREAM} Unknown event type`, {
          case: event.event.case,
        });
    }
  }

  private attemptReconnect(): void {
    if (this.isIntentionallyClosed || this.reconnectTimer !== null) {
      return;
    }

    // Deliberately unbounded. This stream is the app's only push path, so
    // "stop retrying" means "the UI is silently stale until the user reloads" —
    // never the right outcome. Backoff is capped at MAX_RECONNECT_DELAY_MS and
    // the online/visible wake triggers short-circuit it, so a server that comes
    // back (a rebuilt dev backend, a finished deploy) is picked up promptly
    // without hammering one that is still down.
    this.reconnectAttempts++;
    const exponent = Math.min(
      this.reconnectAttempts - 1,
      MAX_RECONNECT_EXPONENT,
    );
    const delay = Math.min(
      BASE_RECONNECT_DELAY_MS * Math.pow(2, exponent) + Math.random() * 1000,
      MAX_RECONNECT_DELAY_MS,
    );

    logger.info(`${LOG_PREFIX_STREAM} Reconnecting`, {
      attempt: this.reconnectAttempts,
      delayMs: delay,
      fromSeq: Number(this.lastSequence),
      chatFromSeq: Number(this.lastChatSequence),
      subscribeChatId: this.subscribedChatId?.slice(0, 8),
    });

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (!this.isIntentionallyClosed) {
        void this.establishConnection();
      }
    }, delay);
  }

  stop(): void {
    logger.info(`${LOG_PREFIX_STREAM} Stopping`);
    this.isIntentionallyClosed = true;
    this.isConnected_ = false;
    this.connectAttemptInFlight = false;
    this.disarmWatchdog();
    this.unbindWakeHandlers();
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.abortController) {
      this.abortController.abort();
      this.abortController = null;
    }
    this.reconnectAttempts = 0;
  }

  isConnected(): boolean {
    return this.isConnected_;
  }

  getLastSequence(): number {
    return Number(this.lastSequence);
  }

  getLastChatSequence(): number {
    return Number(this.lastChatSequence);
  }

  getSubscribedChatId(): string | undefined {
    return this.subscribedChatId;
  }
}

// ============================================================================
// Factory Functions
// ============================================================================

export function createUserStreamingService(
  callbacks: GlobalWebSocketCallbacks,
): UserStreamingService {
  return new UserStreamingService(callbacks);
}

// Re-export types for convenience
export type {
  UserStreamEvent,
  ChatSyncSnapshot,
  ChatUpdateBatch,
  UserSyncInfo,
  UserUpdateBatch,
  ChatUpdateData,
  UserUpdateData,
  ProtoMessage,
};