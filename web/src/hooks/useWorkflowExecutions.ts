/**
 * Hook to fetch workflow execution tree for a chat.
 *
 * Uses a module-level shared cache so multiple components calling
 * useWorkflowExecutions(chatId) for the same chatId share a single
 * fetch cycle instead of creating duplicate API calls.
 *
 * Instead of polling, this hook subscribes to "workflow_executions"
 * refetch events from the chat stream. The backend emits these events
 * when workflow status changes (start, complete, fail, cancel).
 */

import { useCallback, useSyncExternalStore } from "react";
import { chatGrpc, type WorkflowExecutionData } from "../api/chat-grpc";
import { ChatWorkflowStatus } from "../gen/reliant/v1/chat_pb";
import { logger } from "../lib/logger";
import { subscribeToRefetch } from "../store/refetchStore";

interface UseWorkflowExecutionsResult {
  /** The most recent/active workflow (for backwards compat) */
  data: WorkflowExecutionData | null;
  /** All root workflows for this chat, sorted newest first */
  allWorkflows: WorkflowExecutionData[];
  /** Whether any workflow is currently running */
  hasRunningWorkflow: boolean;
  isLoading: boolean;
  error: Error | null;
  refetch: () => Promise<void>;
}

// ---------------------------------------------------------------------------
// Shared module-level cache
// ---------------------------------------------------------------------------

interface SharedState {
  latest: WorkflowExecutionData | null;
  all: WorkflowExecutionData[];
  isLoading: boolean;
  error: Error | null;
}

interface SharedCacheEntry {
  state: SharedState;
  listeners: Set<() => void>;
  subscriberCount: number;
  /** The chatId this entry was created for – used for staleness checks. */
  chatId: string;
  /** Fetching flag to avoid concurrent fetches. */
  fetching: boolean;
  /** Unsubscribe from refetch events. */
  unsubscribeRefetch: (() => void) | null;
}

const sharedCache = new Map<string, SharedCacheEntry>();

// Stable empty state reference — must be returned from getSnapshot when
// there's no data yet so useSyncExternalStore doesn't detect a spurious
// state change and trigger infinite re-renders.
const EMPTY_STATE: SharedState = Object.freeze({
  latest: null,
  all: [],
  isLoading: false,
  error: null,
});

function getOrCreateEntry(chatId: string): SharedCacheEntry {
  let entry = sharedCache.get(chatId);
  if (!entry) {
    entry = {
      state: { latest: null, all: [], isLoading: false, error: null },
      listeners: new Set(),
      subscriberCount: 0,
      chatId,
      fetching: false,
      unsubscribeRefetch: null,
    };
    sharedCache.set(chatId, entry);
  }
  return entry;
}

function notifyListeners(entry: SharedCacheEntry) {
  for (const listener of entry.listeners) {
    listener();
  }
}

function updateState(entry: SharedCacheEntry, partial: Partial<SharedState>) {
  entry.state = { ...entry.state, ...partial };
  notifyListeners(entry);
}

async function fetchShared(entry: SharedCacheEntry, isInitial = false) {
  if (entry.fetching) return;
  entry.fetching = true;

  const fetchChatId = entry.chatId;

  if (isInitial) {
    updateState(entry, { isLoading: true, error: null });
  }

  try {
    const result = await chatGrpc.getWorkflowExecutions(fetchChatId);

    // Staleness check: if entry was cleaned up or replaced while we were fetching, discard.
    const current = sharedCache.get(fetchChatId);
    if (!current || current !== entry) {
      logger.debug("[useWorkflowExecutions] Discarding stale response (entry replaced)", {
        fetchedFor: fetchChatId?.slice(0, 8),
      });
      return;
    }

    updateState(entry, { latest: result.latest, all: result.all, isLoading: false });
  } catch (err) {
    const current = sharedCache.get(fetchChatId);
    if (!current || current !== entry) return;

    logger.error("[useWorkflowExecutions] Failed to fetch", { chatId: fetchChatId, error: err });
    updateState(entry, {
      error: err instanceof Error ? err : new Error("Failed to fetch workflow executions"),
      isLoading: false,
    });
  } finally {
    entry.fetching = false;
  }
}

function startRefetchSubscription(entry: SharedCacheEntry) {
  if (entry.unsubscribeRefetch) return; // Already subscribed

  entry.unsubscribeRefetch = subscribeToRefetch("workflow_executions", () => {
    fetchShared(entry, false);
  });
}

function stopRefetchSubscription(entry: SharedCacheEntry) {
  if (entry.unsubscribeRefetch) {
    entry.unsubscribeRefetch();
    entry.unsubscribeRefetch = null;
  }
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

/**
 * Fetches and maintains the workflow execution tree for a chat.
 * Automatically updates when the backend emits workflow_executions refetch events.
 *
 * Multiple hook instances for the same chatId share a single fetch cycle.
 */
export function useWorkflowExecutions(
  chatId: string | null,
): UseWorkflowExecutionsResult {
  // Subscribe to shared state using useSyncExternalStore for tear-safe reads
  const subscribe = useCallback(
    (onStoreChange: () => void) => {
      if (!chatId) return () => {};

      const entry = getOrCreateEntry(chatId);
      entry.listeners.add(onStoreChange);
      entry.subscriberCount++;

      // First subscriber: kick off initial fetch + subscribe to refetch events
      if (entry.subscriberCount === 1) {
        fetchShared(entry, true);
        startRefetchSubscription(entry);
      }

      return () => {
        entry.listeners.delete(onStoreChange);
        entry.subscriberCount--;

        if (entry.subscriberCount <= 0) {
          stopRefetchSubscription(entry);
          sharedCache.delete(chatId);
        }
      };
    },
    [chatId]
  );

  const getSnapshot = useCallback((): SharedState => {
    if (!chatId) {
      return EMPTY_STATE;
    }
    const entry = sharedCache.get(chatId);
    if (!entry) {
      return EMPTY_STATE;
    }
    return entry.state;
  }, [chatId]);

  const state = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);

  const hasRunningWorkflow = state.all.some(
    (wf) => wf.status === ChatWorkflowStatus.RUNNING,
  );

  const refetch = useCallback(async () => {
    if (!chatId) return;
    const entry = sharedCache.get(chatId);
    if (entry) {
      await fetchShared(entry, false);
    }
  }, [chatId]);

  return {
    data: state.latest,
    allWorkflows: state.all,
    hasRunningWorkflow,
    isLoading: state.isLoading,
    error: state.error,
    refetch,
  };
}
