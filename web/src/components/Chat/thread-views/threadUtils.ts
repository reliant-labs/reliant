/**
 * Thread utilities - color generation and display helpers
 */

import type { ActiveThreadUpdate, RouterDecisionInfo } from "../../../types/streaming";

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