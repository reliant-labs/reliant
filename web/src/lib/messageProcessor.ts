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

/**
 * A tool result resolved by tool-call id, in the minimal shape the processor
 * needs. Tool results arrive as separate TOOL-role messages; the store
 * normalizes them into an index keyed by tool_call_id and injects that index
 * here so the call→result join happens once, at read time, instead of being
 * embedded into the assistant message's blocks by the store.
 */
export interface ResolvedToolResult {
  content: string;
  is_error?: boolean;
  tool_name?: string;
}

export type ToolResultsByCallId = Record<string, ResolvedToolResult>;

/**
 * An ordered slice of a message's content: either a run of text or a run of
 * consecutive tool executions, in the order the blocks actually occurred.
 * This is what preserves text↔tool interleaving — a tool call that happened
 * between two paragraphs renders between them, not after all prose.
 */
export type MessageSegment =
  | { kind: "text"; text: string }
  | { kind: "tools"; executions: ToolExecution[] };

export interface ProcessedMessage {
  id: string;
  text?: string;
  toolExecutions?: ToolExecution[];
  // Ordered timeline of text/tool runs — the canonical render order. `text`
  // and `toolExecutions` above are flattened views kept for copy, visibility
  // checks, and per-execution enrichment; `segments` is what to render.
  segments: MessageSegment[];
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
  resultsByCallId: ToolResultsByCallId = {},
): ProcessedMessage {
  const blocks = message.contentBlocks;

  if (!blocks || blocks.length === 0) {
    return {
      id: message.id,
      segments: [],
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

  // Ordered timeline of what appeared where, preserving text↔tool interleaving.
  // Text entries carry their run of characters; call entries carry a tool-call
  // id (resolved to a ToolExecution after the walk). Tool results attach to
  // their call and never produce a timeline entry.
  type TimelineEntry = { type: "text"; content: string } | { type: "call"; id: string };
  const timeline: TimelineEntry[] = [];

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
      timeline.push({ type: "call", id: callId });

      // Resolve this call's result by id. Preferred source is the external
      // results index the store injects (live-normalized TOOL messages);
      // fall back to a backend-embedded matchedResult (snapshot / REST loads,
      // where the server pairs the result — see proto_converters.go). Same
      // shape either way; name is the call id, matching the historical
      // matchedResult projection (no renderer reads result.name here).
      const indexed = resultsByCallId[call.id];
      const mr = block.matchedResult;
      if (indexed) {
        toolResultsById.set(call.id, {
          tool_call_id: call.id,
          name: call.id,
          content: indexed.content || "",
          is_error: Boolean(indexed.is_error),
        });
      } else if (mr) {
        toolResultsById.set(call.id, {
          tool_call_id: mr.toolCallId,
          name: mr.toolCallId,
          content: mr.content || "",
          is_error: Boolean(mr.isError),
        });
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
          timeline.push({ type: "call", id: result.tool_call_id });
        }
      }
    } else if (block.type === ContentBlockType.TEXT) {
      const textContent = block.content || "";
      text += textContent;
      timeline.push({ type: "text", content: textContent });

      if (block.content === undefined || block.content === "") {
        isStreaming = true;
      }
    }
  }

  // Resolve each renderable tool call once (dedup + skip nameless "still
  // identifying" calls), keyed by id so both the flat list and the ordered
  // segments draw from the same source of truth.
  const executionById = new Map<string, ToolExecution>();
  const toolExecutions: ToolExecution[] = [];

  for (const id of toolCallOrder) {
    if (executionById.has(id)) continue;

    const call = toolCallsById.get(id);
    if (!call) continue;

    // Filter out tool calls without a name — still being identified
    if (!call.name) continue;

    const result = toolResultsById.get(id);
    const approval =
      approvalsById.get(id) ||
      approvals.find((a) => a.content_block_id === call.content_block_id);

    const execution: ToolExecution = { call, result, approval };
    executionById.set(id, execution);
    toolExecutions.push(execution);
  }

  // Walk the timeline into ordered segments, coalescing consecutive entries of
  // the same kind: a run of text blocks becomes one text segment, a run of
  // tool calls becomes one tools segment. This is what keeps a tool call that
  // occurred mid-message from rendering after all the prose.
  const segments: MessageSegment[] = [];
  for (const entry of timeline) {
    if (entry.type === "text") {
      if (entry.content === "") continue; // empty streaming placeholder text
      const last = segments[segments.length - 1];
      if (last?.kind === "text") {
        last.text += entry.content;
      } else {
        segments.push({ kind: "text", text: entry.content });
      }
    } else {
      const execution = executionById.get(entry.id);
      if (!execution) continue; // filtered (nameless) or already placed
      const last = segments[segments.length - 1];
      if (last?.kind === "tools") {
        last.executions.push(execution);
      } else {
        segments.push({ kind: "tools", executions: [execution] });
      }
      executionById.delete(entry.id); // place each execution once
    }
  }

  return {
    id: message.id,
    text: text || undefined,
    toolExecutions: toolExecutions.length > 0 ? toolExecutions : undefined,
    segments,
    hasToolCalls: toolExecutions.length > 0,
    hasText: !!text,
    isStreaming,
  };
}

/**
 * Reference-keyed memo for processMessage, replacing the hand-maintained
 * processedMessages store cache. Keyed by the message object identity, plus
 * the two other inputs that change what processMessage produces: the per-chat
 * tool-result index and the approvals list. The store publishes a NEW object
 * reference for each of those exactly when their content changes (immutable
 * updates) and keeps the reference stable otherwise — so this recomputes when
 * and only when a message would actually render differently, and is never
 * stale.
 *
 * The WeakMap is module-level (survives component unmount), so switching away
 * from and back to a large chat re-reads the cached parse instead of reparsing
 * every message — the tab-switch fast path the old store cache existed for.
 * Entries are GC'd automatically once a message object is dropped.
 */
interface ProcessedMemoEntry {
  resultsByCallId: ToolResultsByCallId;
  approvals: ToolApprovalRequest[];
  processed: ProcessedMessage;
}
const processedMemo = new WeakMap<Message, ProcessedMemoEntry>();

export function getProcessedMessage(
  message: Message,
  resultsByCallId: ToolResultsByCallId = {},
  approvals: ToolApprovalRequest[] = [],
): ProcessedMessage {
  const cached = processedMemo.get(message);
  if (
    cached &&
    cached.resultsByCallId === resultsByCallId &&
    cached.approvals === approvals
  ) {
    return cached.processed;
  }
  const processed = processMessage(message, approvals, resultsByCallId);
  processedMemo.set(message, { resultsByCallId, approvals, processed });
  return processed;
}