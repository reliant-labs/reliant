/**
/**
 * Chat Store Selector Hooks - Type-safe store access
 *
 * These hooks provide safe, encapsulated access to chatStore state.
 * Using these hooks instead of direct store access provides:
 *
 * 1. **Type Safety**: Guaranteed to access the right fields
 * 2. **Optimization**: Pre-configured selectors with proper equality checks
 * 3. **Maintainability**: Single place to change if store structure evolves
 * 4. **Prevention of Footguns**: Can't accidentally read from wrong store
 *
 * ⚠️ IMPORTANT ⚠️
 * Always use these hooks in components instead of useChatStore() directly.
 * Direct store access is only allowed within the store itself and other stores.
 */

import { useMemo } from "react";
import { useChatStore } from "./chatStore";
import { useChat as useChatQuery } from "../hooks/chat-queries";
import { useMessages } from "../hooks/message-queries";
import type {
  Chat,
  Message,
} from "../api/client";
import type { ToolResultsByCallId } from "../lib/messageProcessor";
import type {
  ErrorUpdate,
  InfoUpdate,
  RunOutputUpdate,
} from "../types/streaming";

// Stable empty references to prevent unnecessary re-renders
const EMPTY_ARRAY: never[] = [];
const EMPTY_MAP = new Map<string, any>();
const EMPTY_OBJECT: Record<string, never> = {};

// ============================================================================
// ACTIVE CHAT SELECTORS
// ============================================================================

/**
 * Get the currently active chat ID
 *
 * ⚠️ CRITICAL: This is the ONLY source of truth for active chat.
 * Do NOT read activeChatId from chatNavigationStore - it doesn't exist there!
 */
export function useActiveChatId(): string | null {
  return useChatStore((state) => state.activeChatId);
}

/**
 * Get the currently active chat object (full chat data).
 *
 * activeChatId is UI navigation state and stays in Zustand; the Chat object
 * itself is sourced from the React Query detail cache (the single source of
 * truth for Chat entities).
 */
export function useActiveChat(): Chat | null {
  const activeChatId = useChatStore((state) => state.activeChatId);
  return useChatQuery(activeChatId ?? undefined).data ?? null;
}

/**
 * Check if a specific chat is currently active
 */
export function useIsChatActive(chatId: string): boolean {
  return useChatStore((state) => state.activeChatId === chatId);
}

// ============================================================================
// CHAT LIST SELECTORS
// ============================================================================

/**
 * Get a specific chat by ID.
 *
 * Delegates to the React Query detail cache (chat-queries.useChat) — the single
 * source of truth for Chat objects — while preserving the historical
 * `Chat | undefined` return shape so existing consumers need no change.
 */
export function useChat(chatId: string | undefined): Chat | undefined {
  return useChatQuery(chatId).data;
}

// ============================================================================
// MESSAGE SELECTORS
// ============================================================================

/**
 * Get messages for a specific chat.
 *
 * Reads from the React Query message cache (messageKeys.list) — the single
 * source of truth for a chat's persisted messages. The cache is kept live from
 * the chat stream (snapshot on subscribe/reconnect, incremental thereafter,
 * plus loadMessages / optimistic writes) via the helpers in message-queries.ts;
 * the queryFn is only a cold-start seed. The in-flight streaming placeholder is
 * NOT here — it lives in the streamingMessages slice and is composed at the
 * render layer (see ChatContainer).
 */
export function useChatMessages(chatId: string | undefined): Message[] {
  return useMessages(chatId).data?.messages ?? (EMPTY_ARRAY as Message[]);
}

/**
 * Scroll-back paging state for a chat's message list.
 *
 * `hasOlder` is the server's has_more from the bounded initial snapshot, kept
 * on the message envelope (see message-queries.ts). `isLoadingOlder` is local
 * to the caller: the fetch is an imperative store action, not a query, so the
 * in-flight flag is owned by whoever awaits it (see ChatContainer).
 */
export function useHasOlderMessages(chatId: string | undefined): boolean {
  return useMessages(chatId).data?.hasMore ?? false;
}

/**
 * Get the normalized tool-result index for a chat (tool_call_id -> result).
 * Tool results arrive as separate TOOL messages; the store keeps them here so
 * consumers resolve a tool call's result by id instead of relying on results
 * embedded into assistant message blocks.
 */
export function useToolResultsByCallId(
  chatId: string
): ToolResultsByCallId {
  return useChatStore(
    (state) =>
      state.toolResultsByCallId[chatId] ||
      (EMPTY_OBJECT as ToolResultsByCallId)
  );
}

/**
 * Get currently streaming message for a specific thread in a chat.
 * Thread-aware: each thread can have its own streaming message.
 * @param chatId - The chat ID
 * @param thread - Optional thread ID. If not provided, returns main thread's streaming message.
 */
export function useStreamingMessage(chatId: string, thread?: string): Message | null {
  return useChatStore((state) => {
    const chatStreaming = state.streamingMessages[chatId];
    if (!chatStreaming) return null;
    // Normalize thread key: main thread uses chatId
    const threadKey = !thread || thread === "0" || thread === chatId ? chatId : thread;
    return chatStreaming[threadKey] || null;
  });
}

/**
 * Get all currently streaming messages for a chat (across all threads).
 * Useful for "All" threads view where you want to show all active streams.
 * Uses useMemo internally to avoid creating new arrays on every render.
 */
export function useStreamingMessages(chatId: string): Message[] {
  const streamingRecord = useChatStore((state) => state.streamingMessages[chatId] ?? null);

  return useMemo(() => {
    if (!streamingRecord) return EMPTY_ARRAY as Message[];
    const messages = Object.values(streamingRecord).filter((m): m is Message => m !== null);
    return messages.length === 0 ? (EMPTY_ARRAY as Message[]) : messages;
  }, [streamingRecord]);
}


// ============================================================================
// WORKFLOW & STATUS SELECTORS
// ============================================================================

/**
 * Get error events for a chat
 */
export function useErrorEvents(chatId: string): ErrorUpdate[] {
  return useChatStore(
    (state) => state.errorEvents[chatId] || (EMPTY_ARRAY as ErrorUpdate[])
  );
}

/**
 * Get info events for a chat (notifications shown to user, not saved to thread)
 */
export function useInfoEvents(chatId: string): InfoUpdate[] {
  return useChatStore(
    (state) => state.infoEvents[chatId] || (EMPTY_ARRAY as InfoUpdate[])
  );
}

/**
 * Get run outputs for a chat (workflow run step outputs)
 */
export function useRunOutputs(chatId: string): RunOutputUpdate[] {
  return useChatStore(
    (state) => state.runOutputs[chatId] || (EMPTY_ARRAY as RunOutputUpdate[])
  );
}

/**
 * Whether a chat is showing cached content whose sync for the current stream
 * subscription (a snapshot, or a replay catching up) has not arrived yet.
 */
export function useIsChatSyncing(chatId: string | null | undefined): boolean {
  return useChatStore((state) => !!chatId && state.chatSyncPendingId === chatId);
}

/**
 * Get context usage for a chat's main thread (for compaction indicator)
 * Main thread is identified by chatId itself
 */
export function useContextUsage(
  chatId: string
): { threadTokenCount: number; compactionThreshold: number } | null {
  return useChatStore((state) => {
    const chatUsage = state.contextUsage[chatId];
    if (!chatUsage) return null;
    // Return main thread (chatId) usage, or first available if main not found
    return chatUsage[chatId] || Object.values(chatUsage)[0] || null;
  });
}

/**
 * Get context usage for all threads in a chat (for per-thread indicators)
 */
export function useContextUsageByThread(
  chatId: string
): Record<string, { threadTokenCount: number; compactionThreshold: number }> {
  return useChatStore((state) => state.contextUsage[chatId] || (EMPTY_OBJECT as Record<string, { threadTokenCount: number; compactionThreshold: number }>));
}

// ============================================================================
// DRAFT CONTENT SELECTORS
// ============================================================================

// ============================================================================
// TOOL CALL STATE SELECTORS
// ============================================================================

/**
 * Get tool call states for a chat
 */
export function useToolCallStates(chatId: string) {
  return useChatStore((state) => state.toolCallStates[chatId] || EMPTY_MAP);
}

// ============================================================================
// DISCUSS MODE SELECTORS
// ============================================================================

/**
 * Get discuss mode state for a chat
 */
export function useDiscussMode(chatId: string): boolean {
  return useChatStore((state) => state.discussMode[chatId] ?? false);
}
