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

import { ContentBlockType, ToolCallStatus } from "../gen/reliant/v1/chat_pb";
import type { Attachment, Message } from "../types/chat";
import type { Approval } from "../gen/reliant/v1/approval_pb";

export interface ToolCallData {
  id: string;
  name: string;
  /** Undefined while the call is still streaming and its input has not arrived. */
  input?: Record<string, unknown> | string;
  finished?: boolean;
  content_block_id?: string;
  /**
   * Durable status from the tool_calls table, as of the last time this
   * message was loaded. Present once the backend has written a row for the
   * call; a live status from the streaming store always takes precedence
   * over this when both are available (see ToolExecution.tsx).
   */
  durableStatus?: ToolCallStatus;
  durableStartedAt?: string;
  durableCompletedAt?: string;
  /**
   * For a spawn call, the workflow it started (tool_calls.child_workflow_id).
   * A spawned sub-agent's thread id equals its workflow id, so this IS the
   * thread the call owns — no search required.
   */
  childWorkflowId?: string;
  /**
   * The server left this call's input out (a stub transcript read,
   * ContentBlock.body_elided): `input` is a skeleton — every key kept, long
   * strings cut — enough to label the call, never to render it as its input.
   * The body is read by `content_block_id` (hooks/content-block-bodies.ts).
   */
  inputElided?: boolean;
  /** Byte length of the stored input, when it was elided. */
  inputSize?: number;
}

export interface ToolResultData {
  tool_call_id: string;
  name: string;
  content: string;
  metadata?: string;
  is_error?: boolean;
  /** Images the tool produced, rendered inside the tool card. */
  attachments?: Attachment[];
  /**
   * The server sent a preview of a large result, not the whole thing (the
   * transcript caps tool output; see ContentBlock.truncated). original_size is
   * the full content's byte length; GetToolResult returns the content.
   */
  truncated?: boolean;
  original_size?: number;
  /**
   * The server left this result's body out (ContentBlock.body_elided):
   * `content` is a short stub and `truncated` is set too. The body is read by
   * `block_id` (hooks/content-block-bodies.ts).
   */
  elided?: boolean;
  /** The TOOL_RESULT block this came from. */
  block_id?: string;
}

export interface ToolExecution {
  call: ToolCallData;
  result?: ToolResultData;
  approval?: Approval;
}

// The tool-result index (call -> result join) lives in @reliantlabs/sdk; the
// processor below reads it.
export {
  resolvedToolResultFromBlock,
  foldToolResultMessage,
  foldToolResultImages,
  type ResolvedToolResult,
  type ToolResultsByCallId,
} from "@reliantlabs/sdk/client";
import { resolvedToolResultFromBlock, type ToolResultsByCallId } from "@reliantlabs/sdk/client";

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
  approvals: Approval[] = [],
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
  const approvalsById = new Map<string, Approval>();
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

      // Input stays undefined until the block carries one. A tool call that is
      // still streaming has no input yet, and the renderers key their pending
      // state off `input === undefined` — substituting a placeholder here makes
      // a half-arrived call indistinguishable from one that was invoked with no
      // arguments.
      let input: Record<string, unknown> | string | undefined;
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
        durableStatus: block.toolCallStatus,
        durableStartedAt: block.startedAt,
        durableCompletedAt: block.completedAt,
        childWorkflowId: block.childWorkflowId,
      };
      if (block.bodyElided) {
        call.inputElided = true;
        if (block.bodySize !== undefined) call.inputSize = Number(block.bodySize);
      }

      // Check for streaming state
      if (!call.name || block.input === undefined) {
        isStreaming = true;
      }

      toolCallsById.set(callId, call);
      toolCallOrder.push(callId);
      timeline.push({ type: "call", id: callId });

      // Resolve this call's result by id from the results index the store
      // injects. Results arrive as their own TOOL messages and the server does
      // not copy them onto the call block, so the join by tool_call_id is the
      // only source. Name is the call id (no renderer reads result.name here).
      const indexed = resultsByCallId[call.id];
      if (indexed) {
        toolResultsById.set(call.id, {
          tool_call_id: call.id,
          name: call.id,
          content: indexed.content || "",
          is_error: Boolean(indexed.is_error),
          attachments: indexed.attachments,
          truncated: indexed.truncated,
          original_size: indexed.original_size,
          elided: indexed.elided,
          block_id: indexed.block_id,
        });
      }
    } else if (block.type === ContentBlockType.TOOL_RESULT) {
      const resolved = resolvedToolResultFromBlock(block);
      const result: ToolResultData = {
        tool_call_id: block.toolCallId || "",
        name: block.toolName || "",
        content: resolved.content,
        is_error: Boolean(block.isError),
        truncated: resolved.truncated,
        original_size: resolved.original_size,
        elided: resolved.elided,
        block_id: resolved.block_id,
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
      approvals.find((a) => a.entityId === call.content_block_id);

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
  approvals: Approval[];
  processed: ProcessedMessage;
}
const processedMemo = new WeakMap<Message, ProcessedMemoEntry>();

export function getProcessedMessage(
  message: Message,
  resultsByCallId: ToolResultsByCallId = {},
  approvals: Approval[] = [],
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