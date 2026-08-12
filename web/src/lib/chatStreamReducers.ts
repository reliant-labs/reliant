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

// Statuses a tool reached by actually running to an outcome. A late "cancelled"
// must not overwrite one: cancelling a single tool used to take its siblings
// down with it, and a completed tool repainted as cancelled is a lie about work
// the user already has the results of. Whichever terminal status lands first is
// the one that describes what the tool did.
const SETTLED_TOOL_STATUSES = new Set(["completed", "failed"]);

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

    if (
      prev &&
      SETTLED_TOOL_STATUSES.has(prev.status) &&
      mappedStatus === "cancelled"
    ) {
      continue; // a tool that ran to an outcome was not cancelled
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
        origin: update.origin || prev.origin,
        origin_node_id: update.origin_node_id || prev.origin_node_id,
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
 * Whether a snapshot should REPLACE the cached message list outright, or be
 * merged onto it.
 *
 * Replace is the default: it is the guard against a snapshot for a DIFFERENT
 * (or stale) chat contaminating this list. But the initial snapshot is BOUNDED
 * to the newest N messages, so a mid-session reconnect re-delivers that same
 * bounded window — and replacing then would silently discard every older page
 * the user had scrolled back and loaded, yanking them to the bottom of the chat
 * mid-read.
 *
 * What distinguishes the two cases is overlap: a reconnect snapshot for this
 * chat shares message ids with what we hold, a foreign/stale one shares none.
 * So we preserve history only when both hold:
 *   1. the snapshot overlaps the cached list by at least one id (proves it is
 *      continuous with the history we already have), and
 *   2. the cache holds MORE messages than the snapshot (proves there is loaded
 *      history the snapshot would destroy).
 * Otherwise nothing would be lost by replacing, so we replace.
 *
 * Deliberately keyed on overlap rather than on "the snapshot's newest message
 * is already present": messages that arrive while the stream is down make the
 * newest snapshot message genuinely new, and that is exactly a reconnect — the
 * case this is meant to survive. Those new messages are upserted by the merge.
 *
 * Exported because the snapshot's replace/merge decision is not local to the
 * message list: chatStore rebuilds the per-chat tool-result index from scratch
 * on a replacing snapshot, and that index must be MERGED whenever the messages
 * were, or preserved older assistant messages render tool calls with no results.
 */
export function snapshotReplacesMessages(
  existing: Message[],
  incoming: Message[],
): boolean {
  const existingIds = new Set(existing.map((m) => m.id));
  const overlaps = incoming.some((m) => existingIds.has(m.id));
  const wouldLoseHistory = existing.length > incoming.length;
  return !overlaps || !wouldLoseHistory;
}

/**
 * Merge a batch of persisted messages into a chat's message list.
 * - Snapshot: replace the whole list (prevents cross-chat/stale contamination),
 *   unless it is a reconnect re-delivery of history we already hold — see
 *   snapshotReplacesMessages.
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
  if (isSnapshot && snapshotReplacesMessages(existing, incoming)) {
    return [...incoming];
  }

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
