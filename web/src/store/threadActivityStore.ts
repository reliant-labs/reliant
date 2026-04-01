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

/**
 * Check if a workflow name is a thread metadata record (not a real workflow)
 */
function isThreadMetadataRecord(workflowName?: string): boolean {
  if (!workflowName) return false;
  return workflowName.startsWith("thread:") || workflowName.startsWith("fork:");
}

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
    set((state) => ({
      threads: { ...state.threads, [chatId]: threads },
    })),

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
 * NOTE: To avoid infinite loops from useSyncExternalStore detecting
 * unstable selector results, we:
 * 1. Get the raw threads array from store (stable reference)
 * 2. Compute the derived Set in useMemo outside the selector
 * 3. Use a ref to cache and return stable Set references
 */
export function useActiveThreadIds(chatId: string): Set<string> {
  const threads = useThreadActivityStore(
    (state) => state.threads[chatId] || EMPTY_ARRAY,
  );

  const cacheRef = useRef<{ threads: typeof threads; result: Set<string> }>({
    threads: EMPTY_ARRAY,
    result: EMPTY_SET,
  });

  return useMemo(() => {
    if (threads === cacheRef.current.threads) {
      return cacheRef.current.result;
    }

    const activeIds = new Set<string>();
    for (const thread of threads) {
      if (isThreadMetadataRecord(thread.workflow_name)) continue;
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
  }, [threads]);
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
      if (isThreadMetadataRecord(thread.workflow_name)) continue;
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
 */
export function useChatCurrentActivity(chatId: string): string | null {
  return useThreadActivityStore((state) => {
    const threads = state.threads[chatId] || [];
    for (const thread of threads) {
      if (isThreadMetadataRecord(thread.workflow_name)) continue;
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
 * Map activity names to user-friendly display text.
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
