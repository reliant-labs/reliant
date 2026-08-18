/**
 * Thread utilities - color generation and display helpers
 */

import type { ActiveThreadUpdate, RouterDecisionInfo } from "../../../types/streaming";

/**
 * Whether a thread was created by the spawn tool.
 *
 * Origin is stored on the thread (threads.origin), so this is a lookup rather
 * than an inference. It replaces comparing a workflow row's spawnedByNodeId
 * against the sentinel "spawn_tool": that field records WHICH node produced a
 * workflow, and a thread has more than one workflow row associated with it, so
 * the answer depended on which row a reader happened to look at last.
 *
 * Takes a raw `string` rather than the narrowed ThreadOrigin union, because
 * the same origin reaches this predicate from two directions: the hand-written
 * sidebar types, where it is already a ThreadOrigin, and generated proto
 * (WorkflowExecutionData), where it is a plain `string` — protobuf has no
 * string-union type to generate. Demanding the union here forced every
 * proto-side caller to write `as ThreadOrigin`, which is an unchecked
 * assertion: it silently affirms whatever the wire happened to send, so the
 * one place that forgot it became a type error and the places that remembered
 * it were merely quiet. Accepting `string` moves the comparison to where the
 * value actually is — an equality test against a known literal is total over
 * `string` and needs no narrowing to be correct.
 */
export function isSpawnOrigin(origin: string | undefined): boolean {
  return origin === "spawn";
}

/**
 * Generate a consistent color for a thread based on its ID
 */
export function getThreadColor(thread: string | undefined, isMainThread: boolean): string {
  if (isMainThread) {
    return "hsl(var(--primary))";
  }

  // Handle undefined/empty thread - treat as main thread
  if (!thread || thread.length === 0) {
    return "hsl(var(--primary))";
  }

  // Generate a hue from the thread path hash
  let hash = 0;
  for (let i = 0; i < thread.length; i++) {
    hash = (hash << 5) - hash + thread.charCodeAt(i);
    hash = hash & hash;
  }

  // Use hue values that are visually distinct
  const hue = (Math.abs(hash) % 300) + 30; // 30-330
  return `hsl(${hue}, 65%, 55%)`;
}

/**
 * Format a node ID for display (e.g., "red_loop" -> "Red Loop")
 */
export function formatNodeId(nodeId: string): string {
  return nodeId
    .replace(/[-_]/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

/**
 * Resolve a thread name from activeThreads streaming data.
 * Returns formatted name if found, undefined otherwise.
 */
export function resolveThreadNameFromActiveThreads(
  threadId: string,
  activeThreads: ActiveThreadUpdate[]
): string | undefined {
  const update = activeThreads.find((t) => t.thread === threadId);
  if (!update) return undefined;
  if (update.thread_title) return formatNodeId(update.thread_title);
  if (update.spawned_by_node_id) return formatNodeId(update.spawned_by_node_id);
  return undefined;
}

/**
 * Resolve router decision metadata from activeThreads streaming data.
 */
export function resolveRouterDecisionFromActiveThreads(
  threadId: string,
  activeThreads: ActiveThreadUpdate[]
): RouterDecisionInfo | undefined {
  const update = activeThreads.find((t) => t.thread === threadId);
  return update?.router_decision;
}