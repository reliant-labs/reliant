// Copyright (c) 2025 Reliant Labs
// gRPC client for the user-wide update stream (StreamUserUpdates): chat list
// changes, activity, processes, daemon signals and every other user-level
// event. Per-chat detail has its own stream, one per open chat
// (chat-streaming-grpc.ts), so switching chats never reconnects this one.

import { create } from "@bufbuild/protobuf";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import { ReconnectingStream } from "./stream-connection";
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
} from "../gen/reliant/v1/streaming_pb";
import { type Message as ProtoMessage } from "../gen/reliant/v1/chat_pb";

export {
  RESUME_LIVENESS_GRACE_MS,
  RESUME_REPLACE_AFTER_HIDDEN_MS,
  RESUME_REPLACE_MIN_INTERVAL_MS,
  RESUME_STALE_AFTER_MS,
  isConnectionLevelFailure,
} from "./stream-connection";

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
  ChatStreamCallbacks,
} from "../types/streaming";

import type {
  UserUpdate,
  GlobalWebSocketCallbacks,
} from "../types/streaming";

// Log prefix for filtering/debugging
const LOG_PREFIX_STREAM = "[📡 UserStream]";

// ============================================================================
// Type Converters
// ============================================================================

/**
 * Convert gRPC UserUpdateData to UserUpdate format
 */
export function convertUserUpdateData(update: UserUpdateData): UserUpdate {
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
// User Streaming Service (gRPC)
// ============================================================================

export class UserStreamingService extends ReconnectingStream<UserStreamEvent> {
  private callbacks: GlobalWebSocketCallbacks;
  private projectId: string | undefined = undefined;
  private lastSequence: bigint = 0n;

  constructor(callbacks: GlobalWebSocketCallbacks) {
    super(LOG_PREFIX_STREAM, {
      onStatusChange: (status) => callbacks.onStatusChange(status),
      onError: (error) => callbacks.onError(error),
    });
    this.callbacks = callbacks;
  }

  /**
   * Start the user stream.
   * @param fromSeq User-level sequence to resume from
   * @param projectId Scope updates to this project (omit for all projects)
   */
  start(fromSeq: number = 0, projectId?: string): void {
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

    logger.info(`${LOG_PREFIX_STREAM} Starting user stream`, { fromSeq });

    this.lastSequence = BigInt(fromSeq);
    this.projectId = projectId;
    this.reconnectAttempts = 0;
    this.open();
  }

  protected openStream(signal: AbortSignal): AsyncIterable<UserStreamEvent> {
    const client = connectClient(StreamingService);
    const request = create(StreamUserUpdatesRequestSchema, {
      sinceSeq: this.lastSequence,
      projectId: this.projectId,
    });
    logger.info(`${LOG_PREFIX_STREAM} Connecting to user stream`, {
      sinceSeq: Number(this.lastSequence),
    });
    return client.streamUserUpdates(request, { signal });
  }

  protected describe(): Record<string, unknown> {
    return { fromSeq: Number(this.lastSequence) };
  }

  protected handleEvent(event: UserStreamEvent): void {
    switch (event.event.case) {
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
        // If the stream is aborted mid-batch (e.g. a resume reconnect),
        // the un-acked sequence ensures the server re-sends on reconnect.
        if (batch.updates.length > 0) {
          this.callbacks.onUpdate(batch.updates.map(convertUserUpdateData));
        }
        // latest_sequence is the cursor: the server has delivered everything
        // this subscription should see up to it. Sequences in `updates` skip
        // by design (the stream is project-filtered), so a skip says nothing
        // about loss — the server sees every sequence, backfills anything its
        // hub dropped before sending later updates, and reports the result
        // here. A batch with no updates only advances the cursor.
        if (batch.latestSequence > this.lastSequence) {
          this.lastSequence = batch.latestSequence;
        }
        break;
      }

      case "heartbeat": {
        // Liveness only (lastEventAt is bumped for every event above).
        break;
      }

      default:
        logger.warn(`${LOG_PREFIX_STREAM} Unknown event type`, {
          case: event.event.case,
        });
    }
  }

  stop(): void {
    logger.info(`${LOG_PREFIX_STREAM} Stopping`);
    this.close(true);
  }

  getLastSequence(): number {
    return Number(this.lastSequence);
  }
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
