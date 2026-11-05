// Copyright (c) 2025 Reliant Labs
// The user-wide update stream (StreamUserUpdates): chat list changes, activity,
// processes, daemon signals and every other user-level event. Per-chat detail
// has its own stream, one per open chat (chat-streaming-grpc.ts), so switching
// chats never reconnects this one.
//
// The stream itself is @reliantlabs/sdk's UserStream; this is the app's
// environment around it (its logger, the page-resume signal its transport
// listens to, the routed Connect client).

import { UserStream, type UserStreamCallbacks } from "@reliantlabs/sdk/client";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import { pageActivity } from "../lib/pageActivity";
import {
  StreamingService,
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
} from "@reliantlabs/sdk/client";

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

import type { GlobalWebSocketCallbacks } from "../types/streaming";

export class UserStreamingService extends UserStream {
  constructor(callbacks: GlobalWebSocketCallbacks) {
    super({
      callbacks: callbacks satisfies UserStreamCallbacks,
      client: () => connectClient(StreamingService),
      logger,
      onConnectionLost: () => pageActivity().noteConnectionLost(),
    });
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
