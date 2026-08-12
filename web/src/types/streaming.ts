// Copyright (c) 2025 Reliant Labs
// Type definitions for gRPC streaming updates

import type { Message as ProtoMessage } from "../gen/reliant/v1/chat_pb";
import type { ThreadOrigin } from "../components/Chat/ExecutionSidebar/types";
import type {
  NodeExecutionEventType,
  WorkflowExecutionEventType,
  NodeExecutionStatus,
  WorkflowExecutionStatus,
  ExecutionLogLevel,
  UserUpdateType,
  EntityType,
} from "../gen/reliant/v1/streaming_pb";

// ============================================================================
// Chat Update Types (snake_case from Go's encoding/json)
// ============================================================================

// ToolApprovalUpdate represents an approval update from the backend
export interface ToolApprovalUpdate {
  update_type: "approval";
  id: string;
  chat_id: string;
  approval_type: "tool" | "workflow_step";
  entity_id: string;
  title: string;
  description?: string;
  content_block_id?: string;
  status: "pending" | "approved" | "denied";
  denial_reason?: string;
  sequence_number: number;
  created_at: string;
  responded_at?: string;
  action_taken?: string;
}

// RouterDecisionInfo carries routing decision metadata from a router node.
export interface RouterDecisionInfo {
  workflow: string;
  preset: string;
}

// ActiveThreadUpdate represents an active thread update from the backend
export interface ActiveThreadUpdate {
  update_type: "thread";
  id: string;
  chat_id: string;
  thread: string;
  workflow_id?: string;
  workflow_name?: string;
  run_id?: string;
  agent_name?: string;
  title?: string;
  is_planning_mode: boolean;
  status: string;
  current_activity?: string;
  current_activity_started_at?: string;
  spawned_by_tool_call_id?: string;
  spawned_by_node_id?: string;
  /**
   * How the thread came to exist ("main" | "spawn" | "fork" | "node"),
   * mirroring threads.origin. This is what identifies a spawned sub-agent;
   * spawned_by_node_id records which node produced the workflow.
   */
  origin?: ThreadOrigin;
  origin_node_id?: string;
  thread_title?: string;
  router_decision?: RouterDecisionInfo;
  created_at: string;
  completed_at?: string;
}

// WorkflowStatusUpdate represents workflow execution status updates
export interface WorkflowStatusUpdate {
  update_type: "workflow_status";
  workflow_id: string;
  workflow_name: string;
  status:
    | "started"
    | "completed"
    | "cancelled"
    | "failed"
    | "paused"
    | "expired";
  timestamp: string;
  // Empty string for the root workflow. Lets a consumer tell a root terminal
  // from a child's without a second lookup.
  parent_workflow_id: string;
}

// ToolCallUpdate represents direct tool_call updates from the backend.
//
// tool_call_id is the LLM-issued tool-call id (e.g. "toolu_01..."), and it is
// the ONLY key this channel is addressed by. It exists from the first
// streaming event and survives persistence unchanged, which a content-block
// UUID does not: blocks have no id while streaming and are re-minted when the
// assistant message is saved — which happens BEFORE tools run.
//
// This is a separate identifier space from ToolApprovalUpdate.content_block_id.
// Approvals key on block ids; tool status keys on tool-call ids. Don't mix them.
export interface ToolCallUpdate {
  update_type: "tool_call";
  id: string;
  tool_call_id: string;
  tool_name: string;
  status: "pending" | "executing" | "completed" | "failed" | "denied";
  sequence_number: number;
  requested_at?: string;
  started_at?: string;
  completed_at?: string;
  node_id?: string;
}

// ErrorUpdate represents workflow/activity error events from the backend
export interface ErrorUpdate {
  update_type: "error";
  id: string;
  chat_id: string;
  activity_type: string;
  activity_id: string;
  error_message: string;
  error_summary?: string; // Clean, user-friendly summary (e.g. "Rate limited by the AI provider")
  timestamp: string;
  attempt_number?: number;
  max_attempts?: number;  // Total retry attempts configured (e.g., 5)
  is_retrying?: boolean;  // true if more retry attempts remain (transient error, not exhausted)
  workflow_id?: string;
  /**
   * Thread this error belongs to. Optional because errors emitted before this
   * field existed carry no thread — those stay chat-scoped (see the timeline's
   * error placement) rather than being guessed into one thread.
   */
  thread?: string;
  sequence_number: number;
}

// InfoUpdate represents informational notifications from workflow activities
export interface InfoUpdate {
  update_type: "info" | "warning";
  id: string;
  chat_id: string;
  title: string;
  message: string;
  level: "info" | "warning" | "success";
  timestamp: string;
  sequence_number?: number;
}

// ChatMetadataUpdate represents changes to chat configuration
export interface ChatMetadataUpdate {
  update_type: "chat";
  id: string;
  chat_id: string;
  workflow_name?: string | null;
  state?: string;
  title?: string;
  updated_at: string;
}

// StreamingDelta represents real-time streaming updates from the LLM
export interface StreamingDelta {
  update_type: "streaming_delta";
  delta_type:
    | "message_start"
    | "content_block_start"
    | "content_block_delta"
    | "thinking_block_start"
    | "thinking_block_delta"
    | "tool_use_start"
    | "tool_use_stop"
    | "tool_cancelled"
    | "stream_cancelled";
  block_index: number;
  block_type?: string;
  delta?: string;
  tool_call?: {
    id: string;
    name: string;
    status?: string;
  };
  status?: string;
  thread?: string;
  sequence_number?: number;
  // Delta identity protocol: the server pre-allocates the assistant message id
  // and stamps every delta with it, plus a per-message monotonic sequence.
  // Absent on deltas from old servers — consumers must treat these as optional
  // and fall back to the legacy thread-keyed placeholder path.
  message_id?: string;
  stream_seq?: number;
}

// StreamFinalizedUpdate marks that the stream for a pre-allocated assistant
// message id reached a terminal state (delta identity protocol). Emitted
// exactly once per allocated id — after this, any delta carrying the same
// message_id is a stale tail and must be dropped.
export interface StreamFinalizedUpdate {
  update_type: "stream_finalized";
  message_id: string;
  thread?: string;
  reason: "completed" | "aborted" | "cancelled";
  last_stream_seq?: number;
  sequence_number?: number;
}

// RunOutputUpdate represents output from a workflow run step
export interface RunOutputUpdate {
  update_type: "run_output";
  id: string;
  step_id: string;
  command: string;
  stdout: string;
  stderr: string;
  output: string;
  exit_code: number;
  interrupted: boolean;
  duration: number;
  working_dir: string;
  worktree_id?: string;
  worktree_path?: string;
  unique_activity_id?: string;
  sequence_number: number;
  timestamp: string;
}

// RefetchUpdate represents stream-driven notifications telling the client to re-fetch data
export interface RefetchUpdate {
  update_type: "refetch";
  type: string;
  chat_id?: string;
  sequence_number?: number;
}

// QuestionUpdate represents a question event (ask_user) from the backend
export interface QuestionUpdate {
  update_type: "question";
  question_id: string;
  chat_id: string;
  workflow_id: string;
  step_id: string;
  status: "pending" | "resolved";
  metadata?: string;
}

// ============================================================================
// Workflow Execution State Types (using proto enums directly)
// ============================================================================


export interface NodeExecutionUpdate {
  update_type: "node_execution";
  event_type: NodeExecutionEventType;
  node_id: string;
  node_type: string;
  status: NodeExecutionStatus;
  workflow_id: string;
  chat_id: string;
  parent_node_id?: string;
  activity_id?: string;
  started_at?: number;
  completed_at?: number;
  duration_ms?: number;
  exit_code?: number;
  error_message?: string;
  iteration?: number;
  max_iterations?: number;
  progress_message?: string;
  progress_percent?: number;
  metadata?: Record<string, string>;
  sequence_number?: number;
}

export interface WorkflowExecutionUpdate {
  update_type: "workflow_execution";
  event_type: WorkflowExecutionEventType;
  workflow_id: string;
  workflow_name: string;
  chat_id: string;
  status: WorkflowExecutionStatus;
  parent_workflow_id?: string;
  thread?: string;
  active_nodes?: string[];
  started_at?: number;
  completed_at?: number;
  timestamp?: number;
  sequence_number?: number;
}

export interface ExecutionLogUpdate {
  update_type: "execution_log";
  id: string;
  workflow_id: string;
  chat_id: string;
  node_id?: string;
  level: ExecutionLogLevel;
  message: string;
  timestamp: number;
  source?: string;
  fields?: Record<string, string>;
  sequence_number?: number;
}

// ============================================================================
// Proto Message Update - wraps proto Message with update_type discriminator
// Proto Messages come from sync snapshots with camelCase fields.
// ============================================================================

export interface ProtoMessageUpdate {
  update_type: "message";
  message: ProtoMessage;
}

// ============================================================================
// ChatUpdate - Discriminated union of all update types
// ============================================================================

export type ChatUpdate =
  | ProtoMessageUpdate
  | ToolApprovalUpdate
  | ActiveThreadUpdate
  | WorkflowStatusUpdate
  | ToolCallUpdate
  | ErrorUpdate
  | InfoUpdate
  | ChatMetadataUpdate
  | StreamingDelta
  | StreamFinalizedUpdate
  | RunOutputUpdate
  | NodeExecutionUpdate
  | WorkflowExecutionUpdate
  | ExecutionLogUpdate
  | RefetchUpdate
  | QuestionUpdate;

// ============================================================================
// Connection & Callback Types
// ============================================================================

export type ConnectionStatus =
  | "connecting"
  | "connected"
  | "disconnected"
  | "error";

export interface MessagePaginationInfo {
  total: number;
  hasMore: boolean;
  oldestSeq: number;
}

export interface ContextUsageInfo {
  threadTokenCount: number;
  compactionThreshold: number;
}

export interface ChatWebSocketCallbacks {
  onUpdate: (updates: ChatUpdate[]) => void;
  onError: (error: string) => void;
  onStatusChange: (status: ConnectionStatus) => void;
  onPaginationInfo?: (pagination: MessagePaginationInfo) => void;
  onContextUsage?: (contextUsage: ContextUsageInfo) => void;
}

// ============================================================================
// User Update Types
// ============================================================================

export interface UserUpdate {
  id: string;
  user_id: string;
  sequence_number: number;
  project_id?: string;
  worktree_id?: string;
  chat_id?: string;
  update_type: UserUpdateType;
  entity_type: EntityType;
  entity_id: string;
  data: Record<string, unknown>;
  created_at: string;
}

export interface GlobalWebSocketCallbacks {
  onUpdate: (updates: UserUpdate[]) => void;
  onError: (error: string) => void;
  onStatusChange: (status: ConnectionStatus) => void;
  onSync: (lastSequence: number) => void;
  // Per-chat detail event callbacks (when subscribe_chat_id is set)
  onChatUpdate?: (updates: ChatUpdate[]) => void;
  onChatSnapshot?: (updates: ChatUpdate[]) => void;
  onChatPaginationInfo?: (pagination: MessagePaginationInfo) => void;
  onChatContextUsage?: (contextUsage: ContextUsageInfo) => void;
}