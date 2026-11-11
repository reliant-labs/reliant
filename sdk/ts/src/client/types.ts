// The message shapes the reducers and decoders share. These are the generated
// proto types with `$typeName` optional, so a message can be built in code
// (an optimistic send, a fixture) as well as received.

import type {
  Attachment as ProtoAttachment,
  ContentBlock as ProtoContentBlock,
  ContentBlockBody as ProtoContentBlockBody,
  Message as ProtoMessage,
} from "#gen/reliant/v1/chat_pb.js";

/** A tool_call block never carries its result: the result is its own tool_result block, joined by tool_call_id. */
export type ContentBlock = Omit<ProtoContentBlock, "$typeName"> & {
  $typeName?: string;
};

export type Attachment = ProtoAttachment;

/**
 * How a transcript read sends the bodies of finished tool calls (the API's
 * ToolBodyMode). "full", the default: inputs whole, results previewed at
 * 8 KiB. "stub": each finished call's input is a skeleton and its result a
 * short stub, both flagged `bodyElided` on the block, and thinking is left
 * out; fetchContentBlockBodies returns a body when it is shown.
 */
export type ToolBodies = "full" | "stub";

/** One block's body, as GetContentBlockBodies returns it. */
export type ContentBlockBody = Omit<ProtoContentBlockBody, "$typeName"> & {
  $typeName?: string;
};

export type Message = Omit<ProtoMessage, "contentBlocks" | "$typeName"> & {
  $typeName?: string;
  contentBlocks: ContentBlock[];
};

/** A status the client holds for one tool call, keyed by the LLM's tool_call_id. */
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
  /**
   * True when the client DEDUCED this status rather than being told it. A
   * stream ending under a tool that never reported an outcome is evidence the
   * tool stopped, but only evidence: the tool may have been past the point of
   * no return and gone on to finish, and its result then arrives normally.
   * An inferred cancel therefore yields to a real outcome, while a cancel the
   * server reported outranks a completion racing in behind it.
   */
  inferred?: boolean;
  /**
   * For a spawn call, the workflow it started — and, since a spawned
   * sub-agent's thread id equals its workflow id, the thread its card
   * previews. A durable fact: once known it is never cleared by a later status.
   */
  childWorkflowId?: string;
  error?: string;
}

/** A tool_call update in the shape the status fold takes. */
export interface ToolExecutionStateUpdate {
  update_type: "tool_execution_state";
  id: string;
  chat_id: string;
  tool_call_id: string;
  tool_name: string;
  status: "pending" | "executing" | "completed" | "failed" | "denied" | "cancelled";
  node_id: string;
  sequence_number: number;
  timestamp: string;
  /** The client deduced this status; the only producer is the stream-abort pass. */
  inferred?: boolean;
  /** For a spawn call, the child workflow (= thread) it started. */
  child_workflow_id?: string;
}
