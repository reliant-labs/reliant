/**
 * Refetch Store
 *
 * A lightweight pub/sub system for stream-driven data refetching.
 * The backend emits "refetch" events via the streaming infrastructure
 * when data changes (instead of the frontend polling on intervals).
 *
 * Components subscribe to specific refetch types and re-fetch data
 * when the corresponding event arrives.
 */

import { create } from "zustand";
import { logger } from "../lib/logger";

const LOG_PREFIX = "[Refetch]";

// Refetch types must match the backend RefetchType constants in internal/db/refetch.go
export type RefetchType =
  | "worktree_changes"
  | "workflow_executions"
  | "config_health"
  | "plan_tasks"
  | "file_tree";

export interface RefetchEvent {
  type: RefetchType;
  entityId?: string;
}

// Subscription callback
type RefetchCallback = (event: RefetchEvent) => void;

interface RefetchStoreState {
  // Monotonically increasing counters per refetch type.
  // Components can use these in useEffect deps to trigger re-fetches.
  counters: Record<RefetchType, number>;
}

// External subscriber registry (not in Zustand state to avoid re-renders)
const subscribers = new Map<RefetchType, Set<RefetchCallback>>();

// Debounce timers per (refetch type, entity) — collapses rapid-fire events for
// the SAME entity into one callback. Keying by type alone kept only the last
// entityId in a burst, so a scoped event was silently replaced by any other
// entity's event of the same type within the window. worktree_changes fires on
// every agent tool call, which made that the common case: a workspace's
// "creation settled" signal was routinely overwritten by tool-call noise from
// another workspace, and its subscribers never heard about it.
const debounceTimers = new Map<string, ReturnType<typeof setTimeout>>();
const DEBOUNCE_MS = 300;

function debounceKey(type: RefetchType, entityId?: string): string {
  return `${type}\u0000${entityId ?? ""}`;
}

export const useRefetchStore = create<RefetchStoreState>()(() => ({
  counters: {
    worktree_changes: 0,
    workflow_executions: 0,
    config_health: 0,
    plan_tasks: 0,
    file_tree: 0,
  },
}));

/**
 * Trigger a refetch for the given type.
 * Called by globalUpdatesStore/chatStore when a refetch event arrives from the stream.
 *
 * Optionally scoped by entity (e.g., worktreeId, chatId) so subscribers
 * can ignore events for other entities.
 */
export function triggerRefetch(
  type: RefetchType,
  entityId?: string,
): void {
  const key = debounceKey(type, entityId);
  const existing = debounceTimers.get(key);
  if (existing) {
    clearTimeout(existing);
  }

  const timer = setTimeout(() => {
    debounceTimers.delete(key);

    // Bump the counter (for useEffect-based consumers)
    useRefetchStore.setState((state) => ({
      counters: {
        ...state.counters,
        [type]: state.counters[type] + 1,
      },
    }));

    const event: RefetchEvent = { type, entityId };

    // Notify callback subscribers
    const subs = subscribers.get(type);
    if (subs) {
      for (const cb of subs) {
        try {
          cb(event);
        } catch (err) {
          logger.warn(`${LOG_PREFIX} Subscriber error for ${type}`, {
            error: err,
          });
        }
      }
    }
  }, DEBOUNCE_MS);

  debounceTimers.set(key, timer);
}

/**
 * Subscribe to refetch events of a given type.
 * Returns an unsubscribe function.
 */
export function subscribeToRefetch(
  type: RefetchType,
  callback: RefetchCallback,
): () => void {
  if (!subscribers.has(type)) {
    subscribers.set(type, new Set());
  }
  subscribers.get(type)!.add(callback);

  return () => {
    const subs = subscribers.get(type);
    if (subs) {
      subs.delete(callback);
      if (subs.size === 0) {
        subscribers.delete(type);
      }
    }
  };
}

export function matchesRefetchScope(
  event: RefetchEvent,
  scope: { worktreeId?: string | null; projectId?: string | null },
): boolean {
  if (!event.entityId) {
    return true;
  }

  if (scope.worktreeId) {
    return event.entityId === scope.worktreeId;
  }

  if (scope.projectId) {
    return event.entityId === scope.projectId;
  }

  return true;
}