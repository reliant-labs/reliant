/**
 * Common types for tool renderers
 */

export interface ToolRenderContext {
  toolName: string;
  toolCallId: string;
  /**
   * For a spawn call, the workflow it started — and therefore the thread it
   * owns, since a spawned sub-agent's thread id equals its workflow id.
   * Sourced from tool_calls.child_workflow_id, which the spawn path writes
   * when it creates the child.
   */
  childWorkflowId?: string;
  /**
   * The tool call's arguments, ALWAYS DEFINED for a renderer.
   *
   * ToolCallData.input is optional — it is undefined while the call is still
   * streaming and its arguments have not arrived. That undefined is
   * normalized to `{}` at the single point where this context is built (see
   * ToolExecution.tsx's renderContext), so the dozen-odd renderers
   * downstream never each have to re-handle the streaming case. Narrowing
   * here rather than widening keeps that decision in one place.
   */
  input: Record<string, unknown> | string;
  result?: ToolResultData;
  worktreeId?: string;
  chatId?: string;
  isExpanded: boolean;
  isCompleted: boolean;
  isExecuting: boolean;
  isPreparing: boolean;
  hasFailed: boolean;
  onSelectThread?: (threadId: string | null) => void;
}

export interface ToolResultData {
  tool_call_id?: string;
  name: string;
  content: string;
  metadata?: string;
  is_error?: boolean;
}

/**
 * Props for a tool content renderer
 */
export interface ToolContentProps {
  ctx: ToolRenderContext;
}