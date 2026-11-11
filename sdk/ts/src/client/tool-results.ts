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
  /**
   * The server left this result's body out (a transcript read that asked for
   * `toolBodies: "stub"`, ContentBlock.body_elided): `content` is a short stub
   * preview, enough to label the call but not to show it, and `truncated` is
   * set too. fetchContentBlockBodies(block_id) returns the body.
   */
  elided?: boolean;
  /** The id of the TOOL_RESULT block this came from: what fetchContentBlockBodies takes. */
  block_id?: string;
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
  if (block.id) resolved.block_id = block.id;
  if (block.truncated) {
    resolved.truncated = true;
    resolved.original_size = block.originalSize !== undefined ? Number(block.originalSize) : undefined;
  }
  if (block.bodyElided) resolved.elided = true;
  return resolved;
}

/**
 * Which of two copies of one call's result the index keeps: the fuller one.
 * A stub never displaces a body (an elided copy never replaces one that is
 * not), and a body replaces a stub. Otherwise the copy already held stays:
 * tool_call_results.tool_call_id is a primary key at rest, so a second copy is
 * the same result re-delivered, and first write wins.
 */
export function fullerToolResult(
  held: ResolvedToolResult | undefined,
  incoming: ResolvedToolResult,
): ResolvedToolResult {
  if (!held) return incoming;
  if (held.elided && !incoming.elided) return incoming;
  return held;
}

/**
 * Fold one TOOL-role message into the tool-result index.
 *
 * Blocks are walked in index order, and an IMAGE block is attributed to the
 * most recent TOOL_RESULT block — which is exactly how the backend writes them
 * (createToolContentBlocks emits each result followed by its own images).
 *
 * An entry already in `into` is replaced only by a fuller copy (see
 * fullerToolResult): a duplicate TOOL message is the same result re-delivered,
 * so first write wins, except that a body always replaces a stub and a stub
 * never replaces a body.
 */
export function foldToolResultMessage(
  message: Message,
  into: ToolResultsByCallId,
): void {
  for (const block of message.contentBlocks || []) {
    if (block.type === ContentBlockType.TOOL_RESULT && block.toolCallId) {
      into[block.toolCallId] = fullerToolResult(into[block.toolCallId], resolvedToolResultFromBlock(block));
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
    // A new entry, never a write into the one held: `into` may be a copy of an
    // index whose entries a state still holds (the entry kept may be the held one).
    into[currentCallId] = { ...existing, attachments: [...(existing.attachments || []), attachment] };
  }
}

/**
 * Join a batch of results onto an index without letting a stub displace a
 * body: each entry of `incoming` is kept or dropped by fullerToolResult
 * against the one `held` has. Hands back `held` itself when nothing changes.
 */
export function mergeToolResults(
  held: ToolResultsByCallId,
  incoming: ToolResultsByCallId,
): ToolResultsByCallId {
  let next: ToolResultsByCallId | null = null;
  for (const [id, result] of Object.entries(incoming)) {
    const prior = held[id];
    if (fullerToolResult(prior, result) === prior) continue;
    next ??= { ...held };
    next[id] = result;
  }
  return next ?? held;
}
