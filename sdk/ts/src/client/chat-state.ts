// The state of one chat as the stream builds it, and the actions that change it.
// Everything here is plain data: a store, a hook or a test holds it however it
// likes and feeds actions to `reduceChat` (reduce-chat.ts).

import type {
  ActiveThreadUpdate,
  ChatMetadataUpdate,
  ContextUsageInfo,
  ChatUpdate,
  ErrorUpdate,
  InfoUpdate,
  MessagePaginationInfo,
  NodeExecutionUpdate,
  QuestionUpdate,
  RunOutputUpdate,
  ToolApprovalUpdate,
  WorkflowStatusUpdate,
} from "./events.js";
import type { Logger } from "./logger.js";
import type { ToolResultsByCallId } from "./tool-results.js";
import type { Message, ToolCallState } from "./types.js";

export interface ChatState {
  chatId: string;
  /**
   * The chat sequence the stream has processed up to: what a reconnect resumes
   * from (0: no state it can vouch for, so the server sends a snapshot).
   */
  cursor: bigint;
  /** Between a subscription and the server's `caught_up`. */
  syncPending: boolean;
  /** Persisted messages in canonical order, plus the client's optimistic sends. */
  messages: Message[];
  /** Results of tool calls, by the call's id. */
  toolResults: ToolResultsByCallId;
  /** Live status of tool calls, by the call's id. */
  toolCalls: ReadonlyMap<string, ToolCallState>;
  /** The in-flight assistant message of each thread, built from deltas; replaced when the persisted one arrives. */
  streaming: Readonly<Record<string, Message>>;
  /** Assistant message ids whose stream ended: a delta naming one is a stale tail. */
  finalizedStreamIds: ReadonlySet<string>;
  approvals: ToolApprovalUpdate[];
  threads: ActiveThreadUpdate[];
  /** Questions an agent is blocked on. */
  pendingQuestions: QuestionUpdate[];
  /** Questions asked without waiting. */
  asyncQuestions: QuestionUpdate[];
  errors: ErrorUpdate[];
  info: InfoUpdate[];
  runOutputs: RunOutputUpdate[];
  nodeExecutions: NodeExecutionUpdate[];
  /** Latest status of each workflow in the chat, by workflow id. */
  workflowStatus: Readonly<Record<string, WorkflowStatusUpdate>>;
  /** The newest chat metadata update (title, state), if any arrived. */
  metadata: ChatMetadataUpdate | null;
  pagination: MessagePaginationInfo | null;
  contextUsage: ContextUsageInfo | null;
}

export type ChatAction =
  /**
   * The initial sync of a subscription: replaces the chat's state. A snapshot
   * carries no ephemeral deltas, so by default it also drops the in-flight
   * placeholders (a missed finalize would otherwise leave one on screen).
   * `keepStreaming` leaves them for a host that knows better, e.g. one that
   * knows the run is still going and clears them itself when it is idle.
   */
  | { kind: "snapshot"; updates: ChatUpdate[]; cursor: bigint; keepStreaming?: boolean }
  /** A replay or live batch. `cursor` is the stream's cursor after the batch. */
  | { kind: "updates"; updates: ChatUpdate[]; cursor: bigint }
  /** How much history exists beyond the snapshot's window. */
  | { kind: "pagination"; pagination: MessagePaginationInfo }
  /** The main thread's context usage, for the compaction indicator. */
  | { kind: "contextUsage"; contextUsage: ContextUsageInfo }
  /**
   * A page of history older than what the chat holds, fetched by the host
   * (ChatSession.loadOlder does). Prepended, never replacing: the newer messages
   * stay, and `oldestSeq` can only move backward.
   */
  | { kind: "olderPage"; messages: Message[]; errors: ErrorUpdate[]; total: number; hasMore: boolean }
  /** The server finished the initial sync. */
  | { kind: "caughtUp"; cursor: bigint }
  /** A message the user sent that no server row describes yet. */
  | { kind: "sendStarted"; clientMessageId: string; text: string; createdAt: string }
  /** The send could not be delivered: the entry stays, marked, and sending the same text again reuses its id. */
  | { kind: "sendFailed"; clientMessageId: string }
  /** Remove a send's entry: the user discarded a failed one, or the server queued it for a running thread instead of saving it. */
  | { kind: "sendDiscarded"; clientMessageId: string };

/** Impure inputs of the reducer, injected so it stays pure. */
export interface ReduceEnv {
  /** ISO timestamp of now; stamps a streaming placeholder and an inferred cancel. */
  now?: () => string;
  /** Where the reducer reports what it set aside (a frame of another chat, a re-delivered tool result). Silent by default; never changes the result. */
  log?: Logger;
}
