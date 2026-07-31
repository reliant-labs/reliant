/**
 * Pure reducers for the chat stream (processChatStreamUpdates).
 *
 * Everything here is a pure function over (existing state, updates) — no
 * store access, no cache writes, no side effects. chatStore composes these
 * inside its stream orchestrator; behavior is pinned by
 * chatStore.streamEvents.test.ts / chatStore.messageMerge.test.ts.
 */
import type { Message } from "../api/client";
import { MessageRole, StreamingState } from "../gen/reliant/v1/chat_pb";
import type { ActiveThreadUpdate } from "../types/streaming";
import type {
  ToolCallState,
  ToolExecutionStateUpdate,
} from "../store/chatStore";
import { logger } from "./logger";

// Resolve the streaming message a new batch of deltas should build on for a
// given thread. If the existing message is already finalized (COMPLETE — e.g.
// a cancelled stream), it is dropped so new deltas start a fresh message
// instead of appending to stale content. Both delta-processing call sites (the
// synchronous path and the buffered-flush path) share this rule; keeping it in
// one place ensures they can't drift.
export function resolveStreamingBase(
  streamingForChat: Record<string, Message | null> | undefined,
  threadKey: string,
): Message | null {
  const current = streamingForChat?.[threadKey] || null;
  if (current?.streamingState === StreamingState.COMPLETE) {
    logger.debug("[Streaming] Discarding finalized streaming message before new deltas", {
      thread: threadKey.slice(0, 8),
      oldMsgId: current.id,
    });
    return null;
  }
  return current;
}

/**
 * Generic reducer for the stream-only event logs (errors / info / run outputs /
 * node executions). They all share one shape: snapshot replaces the whole list,
 * incremental upserts by a per-type dedup key, then optionally sort. The four
 * only differ by:
 *   - key:   how to identify "the same entry" for dedup
 *   - merge: how to fold an incoming entry onto an existing one (default: replace)
 *   - sort:  optional comparator applied after folding
 */
export function applyLogSlice<T>(
  existing: T[],
  updates: T[],
  isSnapshot: boolean,
  opts: {
    key: (item: T) => string;
    merge?: (prev: T, next: T) => T;
    sort?: (a: T, b: T) => number;
  },
): T[] {
  const next = isSnapshot ? [] : [...existing];
  for (const update of updates) {
    const k = opts.key(update);
    // A falsy key is not identifiable — never dedup it (always append), matching
    // the original per-reducer guards (e.g. run outputs with no unique_activity_id).
    const idx = k ? next.findIndex((e) => opts.key(e) === k) : -1;
    if (idx >= 0) {
      next[idx] = opts.merge ? opts.merge(next[idx], update) : update;
    } else {
      next.push(update);
    }
  }
  if (opts.sort) next.sort(opts.sort);
  return next;
}

export const bySequenceNumber = (
  a: { sequence_number?: number },
  b: { sequence_number?: number },
) => (a.sequence_number || 0) - (b.sequence_number || 0);

// Tool-call statuses that must not be clobbered by a late "completed": once a
// user cancels or backgrounds a tool, a completion racing in right after must
// not resurrect it.
export const TERMINAL_TOOL_STATUSES = new Set(["cancelled", "backgrounded"]);

/**
 * Fold tool-execution-state updates into the per-call status Map (keyed by
 * tool_call_id). Maps "denied" → "failed", and guards terminal statuses. Pure:
 * returns a new Map, leaving the input untouched. Not snapshot-aware — tool
 * call state is always merged incrementally.
 */
export function applyToolCallStateUpdates(
  existing: Map<string, ToolCallState>,
  updates: ToolExecutionStateUpdate[],
  chatId: string,
): Map<string, ToolCallState> {
  const next = new Map(existing);
  for (const update of updates) {
    const prev = next.get(update.tool_call_id);
    const mappedStatus: ToolCallState["status"] =
      update.status === "denied" ? "failed" : update.status;

    if (
      prev &&
      TERMINAL_TOOL_STATUSES.has(prev.status) &&
      mappedStatus === "completed"
    ) {
      continue; // don't overwrite a terminal state with a stale completion
    }

    next.set(update.tool_call_id, {
      ...prev,
      id: update.tool_call_id,
      sessionId: chatId,
      toolName: update.tool_name,
      status: mappedStatus,
      timestamp: update.timestamp,
    });
  }
  return next;
}

/**
 * Merge active-thread updates into the existing thread list (dedup by id).
 * When a later update (e.g. a completion) omits identity fields, the prior
 * values are preserved so the timeline keeps thread names and routing info.
 * Pure — the caller writes the result to threadActivityStore.
 */
export function mergeActiveThreads(
  existing: ActiveThreadUpdate[],
  updates: ActiveThreadUpdate[],
): ActiveThreadUpdate[] {
  const next = [...existing];
  for (const update of updates) {
    const idx = next.findIndex((t) => t.id === update.id);
    if (idx >= 0) {
      const prev = next[idx];
      next[idx] = {
        ...prev,
        ...update,
        thread_title: update.thread_title || prev.thread_title,
        spawned_by_node_id: update.spawned_by_node_id || prev.spawned_by_node_id,
        spawned_by_tool_call_id:
          update.spawned_by_tool_call_id || prev.spawned_by_tool_call_id,
        router_decision: update.router_decision || prev.router_decision,
      };
    } else {
      next.push(update);
    }
  }
  return next;
}

/**
 * Merge a batch of persisted messages into a chat's message list.
 * - Snapshot: replace the whole list (prevents cross-chat/stale contamination).
 * - Incremental: upsert by id onto `existing`, and drop the optimistic user
 *   placeholder (`optimistic-user-*`) once a real user message arrives.
 * Pure — `existing` comes from the RQ message cache; the result is written back
 * to it. Ordering is applied later by sortMessagesForDisplay at the render layer.
 */
export function mergeMessages(
  existing: Message[],
  incoming: Message[],
  isSnapshot: boolean,
): Message[] {
  if (isSnapshot) return [...incoming];

  let next = [...existing];

  const hasRealUserMessage = incoming.some(
    (m) => m.role === MessageRole.USER && !m.id.startsWith("optimistic-user-"),
  );
  if (hasRealUserMessage) {
    next = next.filter((m) => !m.id.startsWith("optimistic-user-"));
  }

  for (const newMessage of incoming) {
    const idx = next.findIndex((m) => m.id === newMessage.id);
    if (idx >= 0) next[idx] = newMessage;
    else next.push(newMessage);
  }
  return next;
}
