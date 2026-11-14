// Copyright (c) 2025 Reliant Labs

/**
 * What a viewer of a shared chat may see of a tool's result
 * (control-plane docs/design/org-sharing.md §4): nothing, except the results
 * of the built-in tools whose output is the conversation's own state (plans,
 * tasks, questions). The server elides every other result for a viewer
 * (internal/messageproto/viewer.go). This is the client's own boundary, so a
 * result that reaches a viewer whole anyway (an older server, a cache filled
 * before the chat was shared) is still never shown, copied or rendered.
 */

import type { ProcessedMessage, ToolExecution } from "./messageProcessor";

/**
 * The tools whose results a viewer reads whole, matched by EXACT name, as the
 * server's viewerKeptResultTools: an MCP tool is always mcp__<server>__<tool>,
 * so none can take one of these names. viewerResults.test.ts holds the two
 * lists equal.
 */
export const VIEWER_KEPT_RESULT_TOOLS: ReadonlySet<string> = new Set([
  "create_plan",
  "update_plan",
  "get_plan",
  "add_task",
  "update_task",
  "create_subtask",
  "list_tasks",
  "ask_user",
  "request_machine",
]);

interface ResultBody {
  content: string;
  metadata?: string;
  attachments?: unknown[];
  truncated?: boolean;
  original_size?: number;
  elided?: boolean;
  block_id?: string;
}

/**
 * The result a viewer may be shown. For anyone else, or a kept built-in that
 * arrived whole, it is `result` itself. Otherwise it is the same result with
 * every part of its body removed and marked elided: no renderer, label, card,
 * attachment or copy reads it, and there is no block id to fetch it by.
 */
export function viewerSafeResult<R extends ResultBody>(
  result: R | undefined,
  toolName: string,
  viewer: boolean,
): R | undefined {
  if (!viewer || !result) return result;
  if (VIEWER_KEPT_RESULT_TOOLS.has(toolName) && !result.elided) return result;
  return {
    ...result,
    content: "",
    metadata: undefined,
    attachments: undefined,
    truncated: undefined,
    original_size: undefined,
    block_id: undefined,
    elided: true,
  };
}

function safeExecutions(executions: ToolExecution[]): ToolExecution[] {
  return executions.map((execution) => {
    const result = viewerSafeResult(execution.result, execution.call.name, true);
    return result === execution.result ? execution : { ...execution, result };
  });
}

/** `processed` with viewerSafeResult applied to every execution, in every view of it. */
export function viewerSafeMessage(processed: ProcessedMessage, viewer: boolean): ProcessedMessage {
  if (!viewer) return processed;
  return {
    ...processed,
    toolExecutions: processed.toolExecutions && safeExecutions(processed.toolExecutions),
    segments: processed.segments.map((segment) =>
      segment.kind === "tools" ? { ...segment, executions: safeExecutions(segment.executions) } : segment,
    ),
  };
}
