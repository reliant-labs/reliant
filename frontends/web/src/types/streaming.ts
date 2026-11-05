// Copyright (c) 2025 Reliant Labs
// Type definitions for gRPC streaming updates. The events themselves live in
// @reliantlabs/sdk, with the decoders that produce them; this module keeps the
// app's import path and names the callbacks under the names the app uses.

export type {
  ToolApprovalUpdate,
  RouterDecisionInfo,
  ActiveThreadUpdate,
  WorkflowStatusUpdate,
  ToolCallUpdate,
  ErrorUpdate,
  InfoUpdate,
  ChatMetadataUpdate,
  StreamingDelta,
  StreamFinalizedUpdate,
  AgentMessagesDrainedUpdate,
  RunOutputUpdate,
  RefetchUpdate,
  QuestionUpdate,
  NodeExecutionUpdate,
  WorkflowExecutionUpdate,
  ExecutionLogUpdate,
  ProtoMessageUpdate,
  ChatUpdate,
  ConnectionStatus,
  MessagePaginationInfo,
  ContextUsageInfo,
  UserUpdate,
  ChatStreamCallbacks,
} from "@reliantlabs/sdk/client";

import type { UserStreamCallbacks } from "@reliantlabs/sdk/client";

// Callbacks of the user-wide stream (StreamUserUpdates).
export type GlobalWebSocketCallbacks = UserStreamCallbacks;
