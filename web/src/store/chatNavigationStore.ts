/**
/**
 * Chat Navigation Store - Simple queue-based navigation
 *
 * Replaces the complex tab system with a simple LRU queue of chats.
 * - No tabs, just an active chat
 * - Navigate next/prev through recently visited chats
 * - No "close" concept - queue auto-manages
 * - Clean up archived chats/workspaces
 *
 * ⚠️ CRITICAL ARCHITECTURE RULES ⚠️
 *
 * This store manages NAVIGATION STATE ONLY (queue, UI panels, scroll positions).
 * It does NOT own chat data or the "active chat" concept.
 *
 * DO NOT:
 * ❌ Store activeChatId here (it lives in chatStore)
 * ❌ Store chat data, messages, or agents (they live in chatStore)
 * ❌ Duplicate ANY state from chatStore
 *
 * DO:
 * ✅ Read activeChatId via: chatStore.getState().activeChatId
 * ✅ Change activeChatId via: chatStore.selectChat(chatId)
 * ✅ Store only UI state specific to navigation (panels, scroll, queue)
 *
 * WHY: Duplicate state across stores causes double renders and bugs.
 * We learned this the hard way - don't repeat the mistake!
 */

import { create } from "zustand";
import { ChatState } from "../gen/reliant/v1/chat_pb";
import { WorktreeStatus } from "../gen/reliant/v1/worktree_pb";
import { logger } from "../lib/logger";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useProjectStore } from "./projectStore";
import { useWorktreeStore } from "./worktreeStore";
import type { Chat } from "../api/client";
import { useActivityStore, activityToDotState, ChatActivity } from "./activityStore";
import { getCachedChatList, getChatFromCache } from "../hooks/chat-queries";

/**
 * Whether a chat is blocked on the user.
 *
 * Three ways a chat can want you, and they are deliberately treated alike by
 * the triage shortcut: it is waiting on an approval, it failed and needs a
 * decision, or it has said something you have not read. This is the same rule
 * the sidebar's "Needs Attention" sort uses, kept in one place so the shortcut
 * and the list can never disagree about what is waiting.
 */
export function chatNeedsAttention(
  chat: Chat,
  activities: Map<string, ChatActivity>,
): boolean {
  const activity = activities.get(chat.id) ?? ChatActivity.IDLE;
  return (
    activity === ChatActivity.AWAITING_INPUT ||
    activity === ChatActivity.ERROR ||
    chat.unread
  );
}

async function selectChatWithWorktreeContext(chat: Chat): Promise<void> {
  const projectId = useProjectStore.getState().currentProject?.id;
  if (projectId) {
    const worktreeStore = useWorktreeStore.getState();
    if (chat.worktreeId) {
      const targetWorktree = worktreeStore.worktrees.find((worktree) => worktree.id === chat.worktreeId) ?? null;
      if (targetWorktree) {
        await worktreeStore.switchWorktreeContext(projectId, targetWorktree);
      }
    } else {
      await worktreeStore.switchWorktreeContext(projectId, null);
    }
  }

  const { useChatStore } = await import("./chatStore");
  useChatStore.getState().selectChat(chat);
}

/**
 * Get the ordered list of chats matching the exact visual order in the sidebar.
 * This replicates the Sidebar.tsx sorting logic for grouped/flat views.
 * IMPORTANT: This must match the sidebar's filtering logic exactly, including:
 * - State filters (needs_attention, active, idle)
 * - Worktree filtering (exclude chats with missing/completed/abandoned worktrees)
 * - Activity state computation
 */
async function getOrderedChatList(): Promise<Chat[]> {
  const { useChatListPreferencesStore } = await import("./chatListPreferencesStore");
  const { useWorktreeStore } = await import("./worktreeStore");
  const { useProjectStore } = await import("./projectStore");
  // Read the chat list from the React Query cache — the same source the sidebar
  // renders from — so navigation order matches the sidebar exactly.
  const chats = getCachedChatList(
    useProjectStore.getState().currentProject?.id
  );
  const { sortOrder, viewMode, filters } = useChatListPreferencesStore.getState();
  const worktrees = useWorktreeStore.getState().worktrees;
  const activities = useActivityStore.getState().activities;

  // Helper to get worktree for a chat
  const getWorktreeForChat = (chat: Chat) => {
    if (!chat.worktreeId) return null;
    return worktrees.find((w) => w.id === chat.worktreeId) || null;
  };

  // Helper to check if chat is awaiting approval
  const isAwaitingApproval = (chatId: string) =>
    (activities.get(chatId) ?? ChatActivity.IDLE) === ChatActivity.AWAITING_INPUT;

  // Helper to check if chat needs attention
  const needsAttention = (chat: Chat) =>
    isAwaitingApproval(chat.id) || chat.unread;

  // Compute activity state from activityStore (SINGLE SOURCE OF TRUTH)
  const getChatActivityState = (chat: Chat) => {
    return activityToDotState(activities.get(chat.id) ?? ChatActivity.IDLE);
  };

  // Filter to non-archived chats
  let activeChats = chats.filter((c) => c.state !== ChatState.ARCHIVED);

  // Apply worktree filtering (matching Sidebar.tsx logic)
  // Skip chats whose worktrees don't exist or are completed/abandoned
  activeChats = activeChats.filter((chat) => {
    const worktree = getWorktreeForChat(chat);
    // Skip if worktree doesn't exist
    if (!worktree) {
      return false;
    }
    // Skip if worktree is completed/abandoned
    if (
      worktree.status === WorktreeStatus.COMPLETED ||
      worktree.status === WorktreeStatus.ABANDONED
    ) {
      return false;
    }
    return true;
  });

  // Apply state filters (matching Sidebar.tsx logic)
  if (filters.states && filters.states.length > 0) {
    activeChats = activeChats.filter((chat) => {
      const activityState = getChatActivityState(chat);
      // Map activityState to filter states
      if (filters.states!.includes("needs_attention")) {
        return chat.unread || activityState === "awaiting_approval";
      }
      if (filters.states!.includes("active")) {
        return activityState === "thinking";
      }
      if (filters.states!.includes("idle")) {
        return activityState === "idle";
      }
      return true;
    });
  }

  // Sort function matching Sidebar.tsx logic exactly
  const sortChatList = (chatList: Chat[]): Chat[] => {
    return [...chatList].sort((a, b) => {
      // ONLY awaiting_approval floats to top - it requires immediate user action
      // Other states (thinking, streaming) stay in their natural position but get visual indicators
      const aRequiresAction = isAwaitingApproval(a.id);
      const bRequiresAction = isAwaitingApproval(b.id);

      if (aRequiresAction && !bRequiresAction) return -1;
      if (!aRequiresAction && bRequiresAction) return 1;

      // Apply user-selected sort order for all other chats
      switch (sortOrder) {
        case "recent_activity":
          // Most recent last_message_at first (falls back to created_at for new chats)
          return (
            new Date(b.lastMessageAt || b.createdAt).getTime() -
            new Date(a.lastMessageAt || a.createdAt).getTime()
          );
        case "needs_attention_first": {
          // Chats needing attention come first (unread or awaiting_approval)
          const aNeedsAttention = needsAttention(a);
          const bNeedsAttention = needsAttention(b);
          
          if (aNeedsAttention && !bNeedsAttention) return -1;
          if (!aNeedsAttention && bNeedsAttention) return 1;
          
          // Both need attention or neither - sort by lastMessageAt
          return (
            new Date(b.lastMessageAt || b.createdAt).getTime() -
            new Date(a.lastMessageAt || a.createdAt).getTime()
          );
        }
        case "newest_first":
          // Most recent createdAt first
          return new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime();
        case "oldest_first":
          // Oldest createdAt first
          return new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime();
        case "alphabetical_asc":
          // A-Z by title
          return (a.title || "New chat").localeCompare(b.title || "New chat");
        case "alphabetical_desc":
          // Z-A by title
          return (b.title || "New chat").localeCompare(a.title || "New chat");
        default:
          return 0;
      }
    });
  };

  // For flat view, just return sorted list
  if (viewMode === "flat") {
    return sortChatList(activeChats);
  }

  // For grouped view, group by worktree then flatten in visual order (matching Sidebar.tsx)
  const worktreeGroups = new Map<string, { chats: Chat[]; hasActivity: boolean }>(); 
  
  for (const chat of activeChats) {
    const worktree = getWorktreeForChat(chat);
    if (!worktree) continue; // Skip if worktree doesn't exist (shouldn't happen after filtering, but be safe)
    
    const groupKey = worktree.id;
    if (!worktreeGroups.has(groupKey)) {
      worktreeGroups.set(groupKey, { chats: [], hasActivity: false });
    }
    const group = worktreeGroups.get(groupKey)!;
    group.chats.push(chat);
    
    // Check if any chat in group requires immediate action (awaiting_approval)
    // Only awaiting_approval should cause groups to float - other activity states
    // get visual indicators but don't change sort order
    if (isAwaitingApproval(chat.id)) {
      group.hasActivity = true;
    }
  }

  // Sort chats within each group
  worktreeGroups.forEach((group) => {
    group.chats = sortChatList(group.chats);
  });

  // Sort groups: active groups first, then by most recent chat (matching Sidebar.tsx)
  const sortedGroups = Array.from(worktreeGroups.values()).sort((a, b) => {
    // Active groups first
    if (a.hasActivity !== b.hasActivity) {
      return a.hasActivity ? -1 : 1;
    }
    // Then by most recent chat in group (using last_message_at for activity-based sorting)
    const aTime = Math.max(
      ...a.chats.map((c) => new Date(c.lastMessageAt || c.createdAt).getTime())
    );
    const bTime = Math.max(
      ...b.chats.map((c) => new Date(c.lastMessageAt || c.createdAt).getTime())
    );
    return bTime - aTime;
  });

  // Flatten groups into final ordered list
  return sortedGroups.flatMap((group) => group.chats);
}

interface ChatNavigationState {
  // Core navigation state
  // NOTE: activeChatId removed - managed by chatStore only!
  chatQueue: string[]; // LRU: [most recent, ..., oldest]

  // UI state per chat (moved from tabStore)
  showRecentChanges: Record<string, boolean>;

  scrollPosition: Record<string, number>;

  // Core navigation
  navigateToChat: (chatId: string) => void;
  navigateNext: () => Promise<void>;
  navigatePrev: () => Promise<void>;
  /** Jump to the next chat blocked on the user. Returns false when none are. */
  navigateToNextAttention: () => Promise<boolean>;

  // Queue management
  removeFromQueue: (chatId: string) => Promise<void>;
  removeChatsByWorktree: (worktreeId: string) => void;
  clearQueue: () => Promise<void>;

  // UI state management
  toggleRecentChanges: (chatId: string) => void;
  setRecentChangesOpen: (chatId: string, open: boolean) => void;
  setScrollPosition: (chatId: string, position: number) => void;

  // Utility
  getActiveChatId: () => Promise<string | null>;
  restoreFromWorkspaceState: (projectId: string, worktreeId: string | null) => void;
  reset: () => void;
}

const initialState = {
  chatQueue: [],
  showRecentChanges: {},
  scrollPosition: {},
};

/**
 * ⚠️ Navigation Store - Read the architecture rules at the top of this file! ⚠️
 *
 * This store manages navigation UI state only (queue, panels, scroll positions).
 * It does NOT store activeChatId or chat data - those live in chatStore.
 *
 * Safe usage:
 * ✅ navigateNext/Prev() - Navigate through chat history
 * ✅ toggleTasksPanel() - Toggle UI panels
 * ✅ setScrollPosition() - Track scroll state
 * ✅ chatQueue - Read navigation queue
 *
 * ❌ DO NOT add activeChatId or any other state that duplicates chatStore!
 *
 * For chat data access, use hooks from '../store/chatStoreHooks':
 * - useActiveChatId() - Get active chat ID
 * - useSelectChat() - Change active chat
 */
export const useChatNavigationStore = create<ChatNavigationState>((set, get) => ({
  ...initialState,

  // Navigate to a specific chat (moves to front of queue)
  // NOTE: Does NOT set activeChatId - that's managed by chatStore.selectChat()
  // This only updates the navigation queue for next/prev functionality
  navigateToChat: (chatId: string) => {
    logger.info("[ChatNavigation] Navigate to chat", { chatId: chatId.slice(0, 8) });

    set((state) => {
      // Remove chatId from queue if it exists
      const newQueue = state.chatQueue.filter((id) => id !== chatId);
      // Add to front
      newQueue.unshift(chatId);

      return {
        chatQueue: newQueue,
        // activeChatId removed - managed by chatStore only!
      };
    });
    
    // Also update workspace state for persistence
    const projectId = useProjectStore.getState().currentProject?.id;
    const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
    if (projectId) {
      useWorkspaceStateStore.getState().addToChatQueue(projectId, worktreeId, chatId);
    }
  },

  // Navigate to next chat in the list (matches exact sidebar visual order)
  navigateNext: async () => {
    const orderedChats = await getOrderedChatList();
    if (orderedChats.length === 0) {
      logger.warn("[ChatNavigation] Cannot navigate next - no chats available");
      return;
    }

    const { useChatStore } = await import("./chatStore");
    const chatStore = useChatStore.getState();
    const activeChatId = chatStore.activeChatId;

    const currentIndex = orderedChats.findIndex((c) => c.id === activeChatId);
    
    // If no active chat or not found, go to first chat
    if (currentIndex === -1) {
      const firstChat = orderedChats[0];
      logger.info("[ChatNavigation] Navigate next (no current) -> first chat", {
        to: firstChat.id.slice(0, 8),
      });
      await selectChatWithWorktreeContext(firstChat);
      return;
    }

    // Wrap around to beginning
    const nextIndex = (currentIndex + 1) % orderedChats.length;
    const nextChat = orderedChats[nextIndex];

    logger.info("[ChatNavigation] Navigate next", {
      from: activeChatId?.slice(0, 8),
      to: nextChat.id.slice(0, 8),
      currentIndex,
      nextIndex,
      totalChats: orderedChats.length,
    });

    await selectChatWithWorktreeContext(nextChat);
  },

  // Navigate to previous chat in the list (matches exact sidebar visual order)
  navigatePrev: async () => {
    const orderedChats = await getOrderedChatList();
    if (orderedChats.length === 0) {
      logger.warn("[ChatNavigation] Cannot navigate prev - no chats available");
      return;
    }

    const { useChatStore } = await import("./chatStore");
    const chatStore = useChatStore.getState();
    const activeChatId = chatStore.activeChatId;

    const currentIndex = orderedChats.findIndex((c) => c.id === activeChatId);
    
    // If no active chat or not found, go to last chat
    if (currentIndex === -1) {
      const lastChat = orderedChats[orderedChats.length - 1];
      logger.info("[ChatNavigation] Navigate prev (no current) -> last chat", {
        to: lastChat.id.slice(0, 8),
      });
      await selectChatWithWorktreeContext(lastChat);
      return;
    }

    // Wrap around to end
    const prevIndex = currentIndex === 0 ? orderedChats.length - 1 : currentIndex - 1;
    const prevChat = orderedChats[prevIndex];

    logger.info("[ChatNavigation] Navigate prev", {
      from: activeChatId?.slice(0, 8),
      to: prevChat.id.slice(0, 8),
      currentIndex,
      prevIndex,
      totalChats: orderedChats.length,
    });

    await selectChatWithWorktreeContext(prevChat);
  },

  // Jump to the next chat that is waiting on the user.
  //
  // Cycles rather than always landing on the first: starting from the chat
  // after the active one and wrapping means repeated presses walk the whole
  // queue instead of bouncing between the top two. The active chat is checked
  // last so a chat you are already looking at is where you end up only when it
  // is the only one waiting.
  navigateToNextAttention: async () => {
    const orderedChats = await getOrderedChatList();
    if (orderedChats.length === 0) return false;

    const activities = useActivityStore.getState().activities;
    const { useChatStore } = await import("./chatStore");
    const activeChatId = useChatStore.getState().activeChatId;

    const currentIndex = orderedChats.findIndex((c) => c.id === activeChatId);

    // Rotate so the search starts just past the active chat and wraps.
    const rotated =
      currentIndex === -1
        ? orderedChats
        : [
            ...orderedChats.slice(currentIndex + 1),
            ...orderedChats.slice(0, currentIndex + 1),
          ];

    const target = rotated.find((chat) => chatNeedsAttention(chat, activities));

    if (!target) {
      logger.info("[ChatNavigation] No chats need attention");
      return false;
    }

    if (target.id === activeChatId) {
      logger.info("[ChatNavigation] Already on the only chat needing attention");
      return true;
    }

    logger.info("[ChatNavigation] Navigate to chat needing attention", {
      from: activeChatId?.slice(0, 8),
      to: target.id.slice(0, 8),
    });

    await selectChatWithWorktreeContext(target);
    return true;
  },

  // Remove a specific chat from queue (e.g., when archived)
  removeFromQueue: async (chatId: string) => {
    logger.info("[ChatNavigation] Remove from queue", { chatId: chatId.slice(0, 8) });

    const state = get();
    const newQueue = state.chatQueue.filter((id) => id !== chatId);

    // Get activeChatId from chatStore
    const { useChatStore } = await import("./chatStore");
    const activeChatId = useChatStore.getState().activeChatId;

    // If we're removing the active chat, switch to the next one in queue
    if (activeChatId === chatId) {
      const nextChatId = newQueue.length > 0 ? newQueue[0] : null;
      if (nextChatId) {
        // Use chatStore.selectChat() to switch to next chat
        const chat = getChatFromCache(nextChatId);
        if (chat) {
          await selectChatWithWorktreeContext(chat);
        }
      } else {
        // No more chats, clear active chat
        useChatStore.getState().clearCurrentChat();
      }
    }

    // Clean up UI state for removed chat
    set((state) => {
      const newShowRecentChanges = { ...state.showRecentChanges };
      const newScrollPosition = { ...state.scrollPosition };

      delete newShowRecentChanges[chatId];
      delete newScrollPosition[chatId];

      return {
        chatQueue: newQueue,
        showRecentChanges: newShowRecentChanges,
        scrollPosition: newScrollPosition,
      };
    });
  },

  // Remove all chats for a worktree (when workspace is archived)
  removeChatsByWorktree: (worktreeId: string) => {
    logger.info("[ChatNavigation] Remove chats by worktree", { worktreeId });

    // This will be called with a list of chatIds to remove
    // We need access to chatStore to know which chats belong to this worktree
    // For now, this is a placeholder - will be called with specific chatIds
  },

  // Clear entire queue
  clearQueue: async () => {
    logger.info("[ChatNavigation] Clear queue");

    // Clear active chat in chatStore
    const { useChatStore } = await import("./chatStore");
    useChatStore.getState().clearCurrentChat();

    // Clear navigation state
    set({
      chatQueue: [],
      showRecentChanges: {},
      scrollPosition: {},
    });
  },

  // UI state toggles - also persist to workspace state
  toggleRecentChanges: (chatId: string) => {
    const newValue = !get().showRecentChanges[chatId];
    set((state) => ({
      showRecentChanges: {
        ...state.showRecentChanges,
        [chatId]: newValue,
      },
    }));
    
    // Persist to workspace state
    const projectId = useProjectStore.getState().currentProject?.id;
    const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
    if (projectId) {
      useWorkspaceStateStore.getState().updateWorktreeState(projectId, worktreeId, (state) => ({
        showRecentChanges: { ...state.showRecentChanges, [chatId]: newValue },
      }));
    }
  },

  setRecentChangesOpen: (chatId: string, open: boolean) => {
    set((state) => ({
      showRecentChanges: {
        ...state.showRecentChanges,
        [chatId]: open,
      },
    }));
    
    // Persist to workspace state
    const projectId = useProjectStore.getState().currentProject?.id;
    const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
    if (projectId) {
      useWorkspaceStateStore.getState().updateWorktreeState(projectId, worktreeId, (state) => ({
        showRecentChanges: { ...state.showRecentChanges, [chatId]: open },
      }));
    }
  },

  setScrollPosition: (chatId: string, position: number) => {
    set((state) => ({
      scrollPosition: {
        ...state.scrollPosition,
        [chatId]: position,
      },
    }));
    
    // Also save to workspace state for persistence
    const projectId = useProjectStore.getState().currentProject?.id;
    const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
    if (projectId) {
      useWorkspaceStateStore.getState().setScrollPosition(projectId, worktreeId, chatId, position);
    }
  },

  // Utility
  getActiveChatId: async () => {
    // Get activeChatId from chatStore (single source of truth)
    const { useChatStore } = await import("./chatStore");
    return useChatStore.getState().activeChatId;
  },

  /**
   * Restore navigation state from workspace state.
   * Called when switching worktrees to restore that worktree's navigation context.
   */
  restoreFromWorkspaceState: (projectId: string, worktreeId: string | null) => {
    const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
    
    logger.info("[ChatNavigation] Restoring from workspace state", {
      projectId,
      worktreeId,
      queueLength: worktreeState.chatQueue.length,
      activeChatId: worktreeState.activeChatId?.slice(0, 8),
    });
    
    set({
      chatQueue: worktreeState.chatQueue,
      showRecentChanges: worktreeState.showRecentChanges,
      scrollPosition: worktreeState.scrollPositions,
    });
  },

  reset: () => {
    set(initialState);
  },
}));