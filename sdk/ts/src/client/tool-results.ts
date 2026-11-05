// The join between a tool call and its result. Results arrive as their own
// TOOL-role messages, often in a later batch than the call; they are folded
// into an index keyed by tool_call_id, and a reader resolves call -> result
// from it, so the assistant message is never mutated.

import { ContentBlockType } from "#gen/reliant/v1/chat_pb.js";
import type { Attachment, ContentBlock, Message } from "./types.js";

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
  /**
   * Images the tool produced, as attachment metadata ready to render. A tool
   * that generates an image (generate_image) persists it as an attachment and
   * the backend writes a sibling IMAGE content block on the same TOOL message.
   * TOOL messages are never rendered on their own — their content is folded
   * into the assistant's tool-call card — so the image has to travel with the
   * result through this index, or it is persisted and visible to the model
   * while remaining invisible to the user.
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

export type ToolResultsByCallId = Record<string, ResolvedToolResult>;

/**
 * The index entry for one TOOL_RESULT block. The one place a result block's
 * fields are copied into the index, so every path that folds results (load,
 * scroll-back, stream batches) carries the same ones.
 */
export function resolvedToolResultFromBlock(block: ContentBlock): ResolvedToolResult {
  const resolved: ResolvedToolResult = {
    content: block.content || "",
    is_error: block.isError,
    tool_name: block.toolName,
  };
  if (block.truncated) {
    resolved.truncated = true;
    resolved.original_size = block.originalSize !== undefined ? Number(block.originalSize) : undefined;
  }
  return resolved;
}

/**
 * Fold one TOOL-role message into the tool-result index.
 *
 * Blocks are walked in index order, and an IMAGE block is attributed to the
 * most recent TOOL_RESULT block — which is exactly how the backend writes them
 * (createToolContentBlocks emits each result followed by its own images).
 *
 * Existing entries are never overwritten: a duplicate TOOL message is the same
 * result re-delivered, and first-write-wins is the rule the callers already
 * apply.
 */
export function foldToolResultMessage(
  message: Message,
  into: ToolResultsByCallId,
): void {
  for (const block of message.contentBlocks || []) {
    if (block.type === ContentBlockType.TOOL_RESULT && block.toolCallId) {
      into[block.toolCallId] = resolvedToolResultFromBlock(block);
    }
  }
  foldToolResultImages(message, into);
}

/**
 * Attach a TOOL message's IMAGE blocks to the results already indexed from it.
 *
 * Blocks are walked in index order and each image is attributed to the most
 * recent TOOL_RESULT block, which is how the backend writes them: each result
 * is followed by its own images (see createToolContentBlocks).
 *
 * `ownedCallIds`, when given, restricts the fold to call ids this message
 * actually wrote. The live path dedups re-delivered results by first-write-wins,
 * and without that restriction a duplicate message's images would attach to the
 * winning copy's entry.
 */
export function foldToolResultImages(
  message: Message,
  into: ToolResultsByCallId,
  ownedCallIds?: Record<string, string>,
): void {
  const attachmentsById = new Map<string, Attachment>();
  for (const attachment of message.attachments || []) {
    if (attachment?.id) attachmentsById.set(attachment.id, attachment);
  }
  if (attachmentsById.size === 0) return;

  const blocks = [...(message.contentBlocks || [])].sort((a, b) => {
    const indexA = typeof a.index === "number" ? a.index : 0;
    const indexB = typeof b.index === "number" ? b.index : 0;
    return indexA - indexB;
  });

  let currentCallId: string | undefined;
  for (const block of blocks) {
    if (block.type === ContentBlockType.TOOL_RESULT && block.toolCallId) {
      currentCallId = block.toolCallId;
      continue;
    }

    if (block.type !== ContentBlockType.IMAGE || !block.content || !currentCallId) {
      continue;
    }
    if (ownedCallIds && ownedCallIds[currentCallId] !== message.id) continue;

    const existing = into[currentCallId];
    if (!existing) continue;
    const attachment = attachmentsById.get(block.content);
    if (!attachment) continue;
    if (existing.attachments?.some((a) => a.id === attachment.id)) continue;
    existing.attachments = [...(existing.attachments || []), attachment];
  }
}
