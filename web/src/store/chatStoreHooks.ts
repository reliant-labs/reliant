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

import { useMemo, useRef } from "react";
import { useChatStore } from "./chatStore";
import { useActivityStore, ChatActivity } from "./activityStore";
import type {
  Chat,
  Message,
  ToolApprovalRequest,
} from "../api/client";
import type { ProcessedMessage } from "../lib/messageProcessor";
import type {
  ErrorUpdate,
  InfoUpdate,
  RunOutputUpdate,
  NodeExecutionUpdate,
  SkillInvocationUpdate,
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
 * Get the currently active chat object (full chat data)
 */
export function useActiveChat(): Chat | null {
  return useChatStore((state) => {
    if (!state.activeChatId) {
      return null;
    }
    return state.chats.get(state.activeChatId) || null;
  });
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
 * Get the chats Map reference (stable per mutation cycle).
 * Use useChatsList() if you need an array.
 */
export function useChatsMap(): Map<string, Chat> {
  return useChatStore((state) => state.chats);
}

/**
 * Get all chats as an array — safe for rendering.
 * Uses useMemo so the array reference is stable when the Map hasn't changed.
 */
export function useChats(): Chat[] {
  const chatsMap = useChatStore((state) => state.chats);
  return useMemo(() => Array.from(chatsMap.values()), [chatsMap]);
}

/**
 * Get a specific chat by ID
 */
export function useChat(chatId: string | undefined): Chat | undefined {
  return useChatStore((state) => (chatId ? state.chats.get(chatId) : undefined));
}

// ============================================================================
// MESSAGE SELECTORS
// ============================================================================

/**
 * Get messages for a specific chat
 */
export function useChatMessages(chatId: string | undefined): Message[] {
  return useChatStore((state) =>
    chatId
      ? state.messages[chatId] || (EMPTY_ARRAY as Message[])
      : (EMPTY_ARRAY as Message[])
  );
}

/**
 * Get processed messages for a specific chat (pre-parsed for rendering)
 */
export function useProcessedMessages(
  chatId: string
): Map<string, ProcessedMessage> {
  return useChatStore(
    (state) =>
      state.processedMessages[chatId] ||
      (EMPTY_MAP as Map<string, ProcessedMessage>)
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
 * Get the raw streaming messages record for a chat.
 * Returns the object mapping threadKey -> Message | null.
 * This is a stable reference that only changes when the underlying state changes.
 */
export function useStreamingMessagesRecord(chatId: string): Record<string, Message | null> | null {
  return useChatStore((state) => state.streamingMessages[chatId] ?? null);
}

/**
 * Get all currently streaming messages for a chat (across all threads).
 * Useful for "All" threads view where you want to show all active streams.
 * Uses useMemo internally to avoid creating new arrays on every render.
 */
export function useStreamingMessages(chatId: string): Message[] {
  const streamingRecord = useStreamingMessagesRecord(chatId);

  return useMemo(() => {
    if (!streamingRecord) return EMPTY_ARRAY as Message[];
    const messages = Object.values(streamingRecord).filter((m): m is Message => m !== null);
    return messages.length === 0 ? (EMPTY_ARRAY as Message[]) : messages;
  }, [streamingRecord]);
}

/**
 * Get message pagination state for a chat
 */
export function useMessagePagination(chatId: string) {
  return useChatStore((state) => state.messagePagination[chatId]);
}

// ============================================================================
// APPROVAL SELECTORS
// ============================================================================

/**
 * Get pending approvals for a chat
 */
export function usePendingApprovals(chatId: string): ToolApprovalRequest[] {
  return useChatStore(
    (state) =>
      state.pendingApprovals[chatId] || (EMPTY_ARRAY as ToolApprovalRequest[])
  );
}

/**
 * Get pending yield for a chat
 */
export function usePendingYield(chatId: string) {
  return useChatStore((state) => state.pendingYields[chatId] ?? null);
}

/**
 * Get all approvals (pending + completed) for a chat
 */
export function useChatApprovals(chatId: string): ToolApprovalRequest[] {
  return useChatStore(
    (state) => state.approvals[chatId] || (EMPTY_ARRAY as ToolApprovalRequest[])
  );
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
 * Get skill invocation events for a chat
 */
export function useSkillInvocations(chatId: string): SkillInvocationUpdate[] {
  return useChatStore(
    (state) =>
      state.skillInvocations[chatId] ||
      (EMPTY_ARRAY as SkillInvocationUpdate[])
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
 * Get node execution events for a chat (workflow activity lifecycle events)
 */
export function useNodeExecutions(chatId: string): NodeExecutionUpdate[] {
  return useChatStore(
    (state) =>
      state.nodeExecutions[chatId] || (EMPTY_ARRAY as NodeExecutionUpdate[])
  );
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
// PLANNING MODE & AUTO-APPROVE SELECTORS
// Now using chatParamsStore for mode
// ============================================================================

// ============================================================================
// WEBSOCKET & CONNECTION SELECTORS
// ============================================================================


// ============================================================================
// LOADING & ERROR SELECTORS
// ============================================================================

/**
 * Check if store is loading
 */
export function useIsStoreLoading(): boolean {
  return useChatStore((state) => state.isLoading);
}

/**
 * Get global store error
 */
export function useStoreError(): string | null {
  return useChatStore((state) => state.error);
}

// ============================================================================
// ACTION HOOKS (methods that don't require selectors)
// ============================================================================

/**
 * Get chat store actions (methods that modify state)
 *
 * IMPORTANT: This does NOT subscribe to state - it just returns the action methods.
 * Actions are stable references on the store, so we memoize them to prevent
 * creating a new object on every render (which would cause infinite loops).
 */
export function useChatStoreActions() {
  // Memoize the actions object to prevent re-creating it on every render
  // Store methods are stable, so empty dependency array is safe
  return useMemo(() => {
    const store = useChatStore.getState();
    return {
      // Chat management
      loadChats: store.loadChats,
      createChat: store.createChat,
      deleteChat: store.deleteChat,
      renameChat: store.renameChat,

      // Active chat
      selectChat: store.selectChat,
      clearCurrentChat: store.clearCurrentChat,

      // Messages
      sendMessage: store.sendMessage,
      loadMessages: store.loadMessages,
      loadMoreMessages: store.loadMoreMessages,
      branchChat: store.branchChat,
      branchChatToWorktree: store.branchChatToWorktree,

      // Approvals
      approveToolRequest: store.approveToolRequest,
      denyToolRequest: store.denyToolRequest,
      approveAllPending: store.approveAllPending,
      denyAllPending: store.denyAllPending,

      // Tool calls
      cancelToolCall: store.cancelToolCall,
      convertToBackground: store.convertToBackground,

      // Chat control
      stopStreaming: store.stopStreaming,
      cancelChat: store.cancelChat,
      retryConnection: store.retryConnection,

      // Error handling
      clearError: store.clearError,
    };
  }, []); // Empty deps - store methods are stable
}

/**
 * Get the selectChat action directly
 *
 * This is the CORRECT way to change the active chat.
 * Do NOT set activeChatId directly!
 */
export function useSelectChat() {
  return useChatStore((state) => state.selectChat);
}

/**
 * Get the sendMessage action directly
 */
export function useSendMessage() {
  return useChatStore((state) => state.sendMessage);
}

// ============================================================================
// WORKTREE CONTEXT SELECTORS
// ============================================================================

/**
 * Get the pending worktree ID for new chats
 *
 * When the user navigates to a new chat page without sending a message yet,
 * this holds the worktree ID that would be used for that chat.
 * Returns null if on main workspace or no pending worktree selected.
 */
export function usePendingNewChatWorktreeId(): string | null {
  return useChatStore((state) => state.pendingNewChatWorktreeId);
}

/**
 * Get the effective worktree ID for the current context
 *
 * This handles the case where:
 * 1. An active chat exists -> use its worktree_id
 * 2. No active chat but pending worktree selected -> use pendingNewChatWorktreeId
 * 3. No context -> returns null (caller should handle main workspace fallback)
 *
 * This is useful for components that need to know the current workspace
 * even when no chat has been created yet (e.g., new chat page).
 */
export function useEffectiveWorktreeId(): string | null {
  return useChatStore((state) => {
    // First check if there's an active chat with a worktree
    if (state.activeChatId) {
      const activeChat = state.chats.get(state.activeChatId);
      if (activeChat?.worktreeId) {
        return activeChat.worktreeId;
      }
    }
    // Fall back to pending new chat worktree
    return state.pendingNewChatWorktreeId;
  });
}

// ============================================================================
// WORKFLOW ACTIVITY SELECTORS (for WorkflowHub)
// ============================================================================


/**
 * Check if workflow builder chats are actively running.
 * Used by WorkflowHub to show an "Editing" indicator when a workflow's
 * builder assistant is active.
 *
 * @param builderChatIds - Map of workflow name to builder chat ID
 * @returns Set of workflow names whose builder chat is currently running
 */
export function useActiveBuilderChats(
  builderChatIds: Map<string, string>
): Set<string> {
  const prevResultRef = useRef<Set<string>>(EMPTY_BUILDER_SET);
  const prevKeyRef = useRef<string>("");

  const activities = useActivityStore((state) => state.activities);

  return useMemo(() => {
    if (builderChatIds.size === 0) {
      return EMPTY_BUILDER_SET;
    }

    const active = new Set<string>();
    for (const [workflowName, chatId] of builderChatIds) {
      const activity = activities.get(chatId);
      if (activity !== undefined && activity >= ChatActivity.RUNNING) {
        active.add(workflowName);
      }
    }

    // Stable reference: only return new Set if contents changed
    const key = [...active].sort().join(",");
    if (key === prevKeyRef.current) {
      return prevResultRef.current;
    }
    prevKeyRef.current = key;
    prevResultRef.current = active;
    return active;
  }, [builderChatIds, activities]);
}

const EMPTY_BUILDER_SET = new Set<string>();
