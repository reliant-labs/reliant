/**
 * Common types for tool renderers
 */

export interface ToolRenderContext {
  toolName: string;
  toolCallId: string;
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