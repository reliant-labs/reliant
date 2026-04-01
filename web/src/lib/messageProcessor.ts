/**
/**
 * Message Processing Utilities
 *
 * Processes proto Message objects to extract:
 * - Text blocks
 * - Tool calls with their results (ToolExecution objects)
 * - Embedded approvals
 *
 * Processing works directly with typed contentBlocks — no JSON parsing needed.
 */

import { ContentBlockType } from "../gen/reliant/v1/chat_pb";
import type { Message } from "../types/chat";
import type { ToolApprovalRequest } from "../api/client";

export interface ToolCallData {
  id: string;
  name: string;
  input: Record<string, unknown> | string;
  finished?: boolean;
  content_block_id?: string;
}

export interface ToolResultData {
  tool_call_id: string;
  name: string;
  content: string;
  metadata?: string;
  is_error?: boolean;
}

export interface ToolExecution {
  call: ToolCallData;
  result?: ToolResultData;
  approval?: ToolApprovalRequest;
}

export interface ProcessedMessage {
  id: string;
  text?: string;
  toolExecutions?: ToolExecution[];
  // Cached for rendering performance
  hasToolCalls: boolean;
  hasText: boolean;
  isStreaming: boolean;
}

/**
 * Process a single proto Message to extract text, tool calls, and results
 */
export function processMessage(
  message: Message,
  approvals: ToolApprovalRequest[] = [],
): ProcessedMessage {
  const blocks = message.contentBlocks;

  if (!blocks || blocks.length === 0) {
    return {
      id: message.id,
      hasToolCalls: false,
      hasText: false,
      isStreaming: false,
    };
  }

  const toolCallsById = new Map<string, ToolCallData>();
  const toolResultsById = new Map<string, ToolResultData>();
  const approvalsById = new Map<string, ToolApprovalRequest>();
  const toolCallOrder: string[] = [];
  let text = "";
  let isStreaming = false;

  // Sort blocks by index for consistent ordering
  const sortedBlocks = [...blocks].sort((a, b) => {
    const indexA = typeof a.index === "number" ? a.index : 0;
    const indexB = typeof b.index === "number" ? b.index : 0;
    return indexA - indexB;
  });

  for (const block of sortedBlocks) {
    if (block.type === ContentBlockType.TOOL_CALL) {
      const callId = block.toolCallId || block.id || `streaming-tool-${block.index ?? toolCallOrder.length}`;

      if (toolCallsById.has(callId)) continue;

      // Parse input from JSON string if present
      let input: Record<string, unknown> | string = "{}";
      if (block.input) {
        try {
          input = JSON.parse(block.input);
        } catch {
          input = block.input;
        }
      }

      const call: ToolCallData = {
        id: callId,
        name: block.toolName || "",
        input,
        finished: block.input !== undefined,
        content_block_id: block.id,
      };

      // Check for streaming state
      if (!call.name || block.input === undefined) {
        isStreaming = true;
      }

      toolCallsById.set(callId, call);
      toolCallOrder.push(callId);

      // Check for matched_result from preprocessing (tool call → result pairing)
      if (block.matchedResult) {
        const mr = block.matchedResult;
        const result: ToolResultData = {
          tool_call_id: mr.toolCallId,
          name: mr.toolCallId, // matchedResult doesn't have name, use toolCallId
          content: mr.content || "",
          is_error: Boolean(mr.isError),
        };
        toolResultsById.set(call.id, result);
      }
    } else if (block.type === ContentBlockType.TOOL_RESULT) {
      const result: ToolResultData = {
        tool_call_id: block.toolCallId || "",
        name: block.toolName || "",
        content: block.content || "",
        is_error: Boolean(block.isError),
      };

      if (result.tool_call_id && !toolResultsById.has(result.tool_call_id)) {
        toolResultsById.set(result.tool_call_id, result);

        // Create synthetic call for orphaned results
        if (!toolCallsById.has(result.tool_call_id)) {
          const syntheticCall: ToolCallData = {
            id: result.tool_call_id,
            name: result.name,
            input: "{}",
            finished: true,
          };
          toolCallsById.set(result.tool_call_id, syntheticCall);
          toolCallOrder.push(result.tool_call_id);
        }
      }
    } else if (block.type === ContentBlockType.TEXT) {
      const textContent = block.content || "";
      text += textContent;

      if (block.content === undefined || block.content === "") {
        isStreaming = true;
      }
    }
  }

  // Build tool executions preserving order
  const toolExecutions: ToolExecution[] = [];
  const seenIds = new Set<string>();

  for (const id of toolCallOrder) {
    if (seenIds.has(id)) continue;
    seenIds.add(id);

    const call = toolCallsById.get(id);
    if (!call) continue;

    // Filter out tool calls without a name — still being identified
    if (!call.name) continue;

    const result = toolResultsById.get(id);
    const approval =
      approvalsById.get(id) ||
      approvals.find((a) => a.content_block_id === call.content_block_id);

    toolExecutions.push({ call, result, approval });
  }

  return {
    id: message.id,
    text: text || undefined,
    toolExecutions: toolExecutions.length > 0 ? toolExecutions : undefined,
    hasToolCalls: toolExecutions.length > 0,
    hasText: !!text,
    isStreaming,
  };
}

