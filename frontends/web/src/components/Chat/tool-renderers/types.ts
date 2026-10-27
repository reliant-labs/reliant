/**
 * Common types for tool renderers
 */

import type { Attachment } from "../../../types/chat";

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
  /**
   * Present when `result` is a preview of a larger output: resolves to the
   * full content (cached per call). A copy action uses it so the clipboard
   * gets the whole output, not the preview.
   */
  loadFullResult?: () => Promise<string>;
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
  /**
   * Images the tool generated, as attachment metadata. Populated for a tool
   * that persists an attachment (generate_image); the backend writes a sibling
   * IMAGE content block and the store folds it onto the result here.
   */
  attachments?: Attachment[];
  /**
   * The server sent a preview of a large result, not the whole thing (the
   * transcript caps tool output; see ContentBlock.truncated). original_size is
   * the full content's byte length; GetToolResult returns the content.
   */
  truncated?: boolean;
  original_size?: number;
}

/**
 * Props for a tool content renderer
 */
export interface ToolContentProps {
  ctx: ToolRenderContext;
}