// Types for workflow execution visualization
// Based on actual DB schema: workflows, step_executions tables

export type StepStatus = "running" | "completed" | "failed";
export type WorkflowStatus = "running" | "completed" | "failed" | "cancelled";

/**
 * How a thread came to exist. Stored on the thread itself (threads.origin),
 * not inferred from workflow rows.
 *
 * - "main"  — the chat's root thread
 * - "spawn" — created by the spawn tool
 * - "fork"  — forked from a parent thread at an ordinal
 * - "node"  — created by a workflow graph node
 */
export type ThreadOrigin = "main" | "spawn" | "fork" | "node";

/**
 * Step execution - maps to step_executions table
 * output_json contains activity-specific data
 */
export interface StepExecution {
  id: string;
  stepId: string;
  activityName: string;
  status: StepStatus;
  durationMs?: number;
  exitCode?: number;
  success?: boolean;
  createdAt: number;

  // Raw output from activity (from output_json column)
  // Structure depends on activity type:
  // - V2_SaveMessage: { message, message_id, thread, thread_token_count, message_count }
  // - V2_CallLLM: { message, response_text, tool_calls, input_tokens, output_tokens, cache_* }
  // - V2_ExecuteTools: { message, tool_results }
  // - V2_ExecuteRunStep: { stdout, stderr, exit_code, duration, working_dir }
  // - V2_Compact: { message }
  outputJson?: Record<string, unknown>;

  // Loop context - set when step was executed within a loop
  loopNodeId?: string; // Node ID of the loop that spawned this step
  loopIteration?: number; // Iteration index within the loop (0-indexed)

  // For loop steps (computed in UI from loop context)
  isLoop?: boolean;
  loopIterations?: WorkflowExecution[];
}

/**
 * Workflow execution - maps to workflows table
 */
export interface WorkflowExecution {
  id: string;
  workflowName: string;
  thread: string;
  threadTitle?: string; // Human-readable title for the thread (e.g., preset name or node ID)
  status: WorkflowStatus;
  parentId?: string;
  spawnedByNodeId?: string; // Node ID that spawned this child workflow
  /**
   * How this workflow's thread came to exist. Read from the threads table,
   * which owns thread identity.
   *
   * This is the field that answers "is this a spawned sub-agent?".
   * spawnedByNodeId answers a different question — WHICH node produced the
   * workflow — and comparing it against a sentinel string was how a spawn
   * thread could be mistaken for a graph-node thread.
   */
  origin?: ThreadOrigin;
  originNodeId?: string; // Node that created the thread, when origin is "node"
  forkedFromThread?: string; // Thread this was forked from (if fork, not new)
  parentThread?: string; // Parent thread ID (set for both fork and new child threads)
  createdAt: number;
  completedAt?: number;
  messageCount: number;
  children: WorkflowExecution[];
  steps: StepExecution[];
  iteration?: number;
  maxIterations?: number;
}


