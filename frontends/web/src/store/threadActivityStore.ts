/**
 * Thread Activity Store — per-chat thread activity state
 *
 * Tracks which threads are active and their current activity for
 * currently-viewed chats. This data comes from per-chat detail events
 * delivered through the unified gRPC stream (when subscribe_chat_id is set)
 * and is only needed for the open chat view,
 * NOT for the sidebar (which uses activityStore).
 *
 * Previously this was part of chatStore.activeThreads — moved here
 * (Phase 6) to separate thread-level detail from global chat state.
 */

import { create } from "zustand";
import { useMemo, useRef } from "react";
import type { ActiveThreadUpdate } from "../types/streaming";
import { useIsChatRunning } from "./activityStore";

// Stable empty references to prevent unnecessary re-renders
const EMPTY_ARRAY: ActiveThreadUpdate[] = [];
const EMPTY_SET = new Set<string>();

// ============================================================================
// Store
// ============================================================================

interface ThreadActivityState {
  // Raw thread updates keyed by chatId — only populated for open/streaming chats
  threads: Record<string, ActiveThreadUpdate[]>;

  // Actions
  setThreads: (chatId: string, threads: ActiveThreadUpdate[]) => void;
  clearThreads: (chatId: string) => void;
  clearAll: () => void;
}

export const useThreadActivityStore = create<ThreadActivityState>((set) => ({
  threads: {},

  setThreads: (chatId, threads) =>
    set((state) => {
      // mergeActiveThreads returns the caller's existing reference when a
      // merge is a no-op, so a reference-equal incoming array means nothing
      // changed. Return the same state object — a zustand `set` that
      // produces a new object still notifies subscribers even when its
      // values are unchanged, so this is what actually stops the re-render.
      if (state.threads[chatId] === threads) return state;
      return { threads: { ...state.threads, [chatId]: threads } };
    }),

  clearThreads: (chatId) =>
    set((state) => {
      const next = { ...state.threads };
      delete next[chatId];
      return { threads: next };
    }),

  clearAll: () => set({ threads: {} }),
}));

// ============================================================================
// Selector Hooks
// ============================================================================

/**
 * Get raw ActiveThreadUpdate[] for a chat.
 * Used by components that need thread name resolution (InterleavedTimeline, useThreads, ChatInput).
 */
export function useActiveThreads(chatId: string): ActiveThreadUpdate[] {
  return useThreadActivityStore(
    (state) => state.threads[chatId] || EMPTY_ARRAY,
  );
}

/**
 * Get the set of active thread IDs for a chat.
 * Use this for ThreadTabs to determine which tabs show activity pulse.
 *
 * Gated internally on the chat-level authority (activityStore): when the
 * chat is not RUNNING/AWAITING_INPUT the set is empty, regardless of what
 * per-thread records say. Thread records persist across pause/idle (they
 * carry names/titles for the timeline) and their status can lag the
 * chat-level IDLE event — the gate ensures every consumer sees the same
 * answer as the sidebar dot instead of a transiently-diverging one.
 *
 * NOTE: To avoid infinite loops from useSyncExternalStore detecting
 * unstable selector results, we:
 * 1. Get the raw threads array from store (stable reference)
 * 2. Compute the derived Set in useMemo outside the selector
 * 3. Use a ref to cache and return stable Set references
 */
export function useActiveThreadIds(chatId: string): Set<string> {
  const isRunning = useIsChatRunning(chatId);
  const threads = useThreadActivityStore(
    (state) => state.threads[chatId] || EMPTY_ARRAY,
  );

  const cacheRef = useRef<{ threads: typeof threads; result: Set<string> }>({
    threads: EMPTY_ARRAY,
    result: EMPTY_SET,
  });

  return useMemo(() => {
    if (!isRunning) return EMPTY_SET;
    if (threads === cacheRef.current.threads) {
      return cacheRef.current.result;
    }

    const activeIds = new Set<string>();
    for (const thread of threads) {
      if (
        (thread.status === "running" || thread.status === "active") &&
        thread.thread
      ) {
        activeIds.add(thread.thread);
      }
    }

    const result = activeIds.size > 0 ? activeIds : EMPTY_SET;
    cacheRef.current = { threads, result };
    return result;
  }, [isRunning, threads]);
}

/**
 * Check if a specific thread is active.
 * Returns true if:
 * - threadId is null ("All" view) and chat is active
 * - threadId === chatId (main thread) and chat is active
 * - threadId is a specific child thread that is currently running
 */
export function useIsThreadActive(
  chatId: string,
  threadId: string | null,
): boolean {
  const isRunning = useIsChatRunning(chatId);

  const threads = useThreadActivityStore(
    (state) => state.threads[chatId] || EMPTY_ARRAY,
  );

  return useMemo(() => {
    if (!isRunning) return false;

    // null = "All" view - active if chat is active
    if (threadId === null) return true;

    // Main thread - active if ANY child is active
    if (threadId === chatId) return true;

    // Specific child thread - check activeThreads
    for (const thread of threads) {
      if (
        (thread.status === "running" || thread.status === "active") &&
        thread.thread === threadId
      ) {
        return true;
      }
    }
    return false;
  }, [isRunning, threadId, chatId, threads]);
}

/**
 * Get the current activity description (e.g., "Thinking", "Running tools").
 *
 * Gated internally on the chat-level authority (activityStore): a stale
 * "running" thread record can no longer surface an activity string after
 * the chat went IDLE, so the thinking indicator's text and its visibility
 * (useIsThreadActive) can never disagree.
 */
export function useChatCurrentActivity(chatId: string): string | null {
  const isRunning = useIsChatRunning(chatId);
  return useThreadActivityStore((state) => {
    if (!isRunning) return null;
    const threads = state.threads[chatId] || [];
    for (const thread of threads) {
      if (
        (thread.status === "running" || thread.status === "active") &&
        thread.current_activity
      ) {
        return thread.current_activity;
      }
    }
    return null;
  });
}

/**
 * Map workflow activity names to user-friendly display text.
 *
 * This is the single client-side map — do not duplicate it in components.
 * Unknown names deliberately fall back to "Thinking" so new server-side
 * activities degrade gracefully.
 *
 * TODO: replace with server-driven display text (a protocol change: the
 * thread update would carry a display string instead of the internal
 * activity handler name, eliminating this V2_-prefixed name zoo).
 */
export function getActivityDisplayText(activity: string | null): string | null {
  if (!activity) return null;

  const activityMap: Record<string, string> = {
    Compact: "Summarizing conversation",
    V2_Compact: "Summarizing conversation",
    ProcessUserMessage: "Thinking",
    ProcessToolCalls: "Running tools",
    HandleApprovals: "Waiting for approval",
    ExecuteToolCall: "Running tool",
    CallLLM: "Thinking",
    V2_CallLLM: "Thinking",
    ExecuteTools: "Running tools",
    V2_ExecuteTools: "Running tools",
  };

  return activityMap[activity] || "Thinking";
}