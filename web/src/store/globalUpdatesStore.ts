/**
 * Global Updates Store
 * 
 * Manages the global connection for user-level updates.
 * Phase 12: Now uses gRPC streaming instead of WebSocket.
 * This replaces per-chat polling for chat state changes and enables
 * real-time updates for chats, projects, worktrees, and background processes.
 */

import { create } from "zustand";
// Phase 12: Use gRPC streaming service instead of WebSocket
import { UserStreamingService } from "../api/streaming-grpc";
import type { UserUpdate, ChatUpdate, ConnectionStatus, MessagePaginationInfo, ContextUsageInfo } from "../types/streaming";
import { useChatStore, initGlobalUpdatesStoreRef } from "./chatStore";
import type { Chat } from "../api/client";
import { ChatState } from "../gen/reliant/v1/chat_pb";
import { useActivityStore, ChatActivity } from "./activityStore";
import { useThreadActivityStore } from "./threadActivityStore";
import { BackgroundProcessStatus } from "../gen/reliant/v1/common_pb";
import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";

import { usePackageCommandsStore } from "./packageCommandsStore";
import { useProcessStore } from "./processStore";
import { useWorktreeStore } from "./worktreeStore";
import { useProjectStore } from "./projectStore";
import { useChatNavigationStore } from "./chatNavigationStore";
import type { BackgroundProcess } from "../api/background-grpc";
import { logger } from "../lib/logger";
import { showWorkflowCompletionNotification, showApprovalRequiredNotification, getNotificationPermission } from "../lib/notifications";
import { getNotificationSoundOptions, useNotificationStore } from "./notificationStore";
import { triggerRefetch, type RefetchType } from "./refetchStore";
import { setDaemonLastSeen } from "../api/grpc-client";

const LOG_PREFIX = "[🌐 GlobalUpdates]";

// Timestamp when the app started - only notify for events after this
const appStartTime = Date.now();

/**
 * Trigger archived chats load through the store, which has its own singleflight
 * and archivedChatsLoaded guard. This replaces the old approach of calling
 * api.chatsV2.listArchived() directly, which bypassed deduplication and caused
 * redundant 1.1 MB fetches racing with the Sidebar's initial load.
 */
function scheduleArchivedChatHydration(_chatId: string): void {
  // Route through the store — it will no-op if already loaded, and uses
  // singleflight to deduplicate concurrent calls.
  void useChatStore.getState().loadArchivedChats();
}

interface GlobalUpdatesState {
  // Connection state
  connectionStatus: ConnectionStatus;
  lastSequence: number;
  
  // Last known daemon heartbeat (unix seconds). Set via DAEMON_HEARTBEAT events.
  daemonLastSeen: number | null;
  
  // Currently subscribed chat ID for detail events
  subscribedChatId: string | null;
  
  // Unified gRPC streaming service
  wsService: UserStreamingService | null;
  
  // Actions
  setLastSequence: (seq: number) => void;
  connect: () => void;
  disconnect: () => void;
  subscribeToChatDetails: (chatId: string) => void;
  unsubscribeFromChatDetails: () => void;
  
  // Internal handlers
  handleUpdate: (updates: UserUpdate[]) => void;
  handleChatUpdate: (updates: ChatUpdate[]) => void;
  handleChatSnapshot: (updates: ChatUpdate[]) => void;
  handleChatPaginationInfo: (pagination: MessagePaginationInfo) => void;
  handleChatContextUsage: (contextUsage: ContextUsageInfo) => void;
  handleStatusChange: (status: ConnectionStatus) => void;
  handleSync: (lastSequence: number) => void;
  handleError: (error: string) => void;
}

export const useGlobalUpdatesStore = create<GlobalUpdatesState>((set, get) => ({
  connectionStatus: "disconnected",
  lastSequence: 0,
  daemonLastSeen: null,
  subscribedChatId: null,
  wsService: null,

  setLastSequence: (seq: number) => {
    set({ lastSequence: seq });
  },

  connect: () => {
    const state = get();
    
    // Already connected or connecting
    if (state.wsService?.isConnected() || state.connectionStatus === "connecting") {
      logger.debug(`${LOG_PREFIX} Already connected or connecting`);
      return;
    }

    // Guard: don't connect with sinceSeq=0 if chats haven't loaded yet.
    // The ModernApp effect ensures loadChats() completes (setting lastSequence)
    // before calling connect(). If something else calls connect() early
    // (e.g. subscribeToChatDetails before loadChats), bail out — the
    // ModernApp effect will handle the connection properly.
    if (state.lastSequence === 0 && !useChatStore.getState().hasLoaded) {
      logger.info(`${LOG_PREFIX} Deferring connect — chats not loaded yet (lastSequence=0)`);
      return;
    }

    logger.info(`${LOG_PREFIX} Connecting unified gRPC stream`, {
      lastSequence: state.lastSequence,
      subscribedChatId: state.subscribedChatId?.slice(0, 8),
    });

    const wsService = new UserStreamingService({
      onUpdate: (updates) => get().handleUpdate(updates),
      onStatusChange: (status) => get().handleStatusChange(status),
      onSync: (lastSequence) => get().handleSync(lastSequence),
      onError: (error) => get().handleError(error),
      // Per-chat detail event callbacks
      onChatUpdate: (updates) => get().handleChatUpdate(updates),
      onChatSnapshot: (updates) => get().handleChatSnapshot(updates),
      onChatPaginationInfo: (pagination) => get().handleChatPaginationInfo(pagination),
      onChatContextUsage: (contextUsage) => get().handleChatContextUsage(contextUsage),
    });

    set({ wsService });
    // Forward subscribedChatId and projectId so the initial connection
    // includes the chat subscription and project scope — avoids redundant
    // disconnect/reconnect and filters catch-up replay to this project.
    const projectId = useProjectStore.getState().currentProject?.id;
    wsService.start(state.lastSequence, state.subscribedChatId ?? undefined, 0, projectId);
  },

  disconnect: () => {
    const state = get();
    if (state.wsService) {
      logger.info(`${LOG_PREFIX} Disconnecting unified gRPC stream`);
      state.wsService.stop();
      set({ wsService: null, connectionStatus: "disconnected", subscribedChatId: null });
    }
  },

  subscribeToChatDetails: (chatId: string) => {
    const state = get();
    if (state.subscribedChatId === chatId) {
      logger.debug(`${LOG_PREFIX} Already subscribed to chat`, { chatId: chatId.slice(0, 8) });
      return;
    }

    logger.info(`${LOG_PREFIX} Subscribing to chat detail events`, {
      chatId: chatId.slice(0, 8),
      previousChatId: state.subscribedChatId?.slice(0, 8),
    });

    set({ subscribedChatId: chatId });

    if (state.wsService) {
      state.wsService.subscribeToChatDetails(chatId);
    } else {
      // Not connected yet — connect with subscription
      get().connect();
    }
  },

  unsubscribeFromChatDetails: () => {
    const state = get();
    if (!state.subscribedChatId) return;

    const oldChatId = state.subscribedChatId;
    logger.info(`${LOG_PREFIX} Unsubscribing from chat detail events`, {
      chatId: oldChatId.slice(0, 8),
    });

    // Clear thread activity for the old chat
    useThreadActivityStore.getState().clearThreads(oldChatId);

    set({ subscribedChatId: null });

    if (state.wsService) {
      state.wsService.unsubscribeFromChatDetails();
    }
  },

  handleUpdate: (updates) => {
    logger.info(`${LOG_PREFIX} Processing ${updates.length} updates`, {
      types: updates.map(u => u.update_type),
    });

    for (const update of updates) {
      // Track the highest sequence number
      if (update.sequence_number > get().lastSequence) {
        set({ lastSequence: update.sequence_number });
      }

      // Route updates to appropriate stores
      switch (update.update_type) {
        case UserUpdateType.CHAT_STATE_CHANGE:
          handleChatStateChange(update);
          break;
        case UserUpdateType.CHAT_CONFIG_CHANGED:
          handleChatConfigChanged(update);
          break;
        case UserUpdateType.CHAT_CREATED:
          handleChatCreated(update);
          break;
        case UserUpdateType.CHAT_TITLE_CHANGED:
          handleChatTitleChanged(update);
          break;
        case UserUpdateType.CHAT_DELETED:
          handleChatDeleted(update);
          break;
        case UserUpdateType.PROCESS_STARTED:
          handleProcessStarted(update);
          break;
        case UserUpdateType.PROCESS_COMPLETED:
          handleProcessCompleted(update);
          break;
        case UserUpdateType.PROCESS_FAILED:
          handleProcessFailed(update);
          break;
        case UserUpdateType.PROCESS_PORT_CHANGED:
          handleProcessPortChanged(update);
          break;
        case UserUpdateType.CHAT_ACTIVITY_CHANGED:
          handleChatActivityChanged(update);
          break;
        case UserUpdateType.REFETCH:
          handleRefetch(update);
          break;
        case UserUpdateType.DAEMON_HEARTBEAT: {
          const data = typeof update.data === "string" ? JSON.parse(update.data) : update.data;
          if (data?.last_heartbeat) {
            const ts = data.last_heartbeat as number;
            set({ daemonLastSeen: ts });
            setDaemonLastSeen(ts);
          }
          break;
        }
        default:
          logger.debug(`${LOG_PREFIX} Unhandled update type: ${update.update_type}`);
      }
    }
  },

  handleStatusChange: (status) => {
    const prevStatus = get().connectionStatus;
    logger.info(`${LOG_PREFIX} Connection status: ${prevStatus} -> ${status}`);
    set({ connectionStatus: status });

    // On reconnect, refresh chat state to ensure sync
    // This handles the case where events were missed during disconnection
    if (status === "connected" && prevStatus !== "connected" && prevStatus !== "connecting") {
      logger.info(`${LOG_PREFIX} Reconnected - refreshing chat state to ensure sync`);
      useChatStore.getState().loadChats().catch((err) => {
        logger.warn(`${LOG_PREFIX} Failed to refresh chats on reconnect`, { error: err });
      });
    }
  },

  handleSync: (lastSequence) => {
    logger.info(`${LOG_PREFIX} Sync complete, lastSequence: ${lastSequence}`);
    set({ lastSequence });
  },

  handleError: (error) => {
    logger.error(`${LOG_PREFIX} Error: ${error}`);
  },

  handleChatUpdate: (updates) => {
    const chatId = get().subscribedChatId;
    if (!chatId) {
      logger.warn(`${LOG_PREFIX} Received chat updates but no chat is subscribed`);
      return;
    }

    // Route incremental chat updates (merge semantics)
    useChatStore.getState().processChatStreamUpdates(chatId, updates);
  },

  handleChatSnapshot: (updates) => {
    const chatId = get().subscribedChatId;
    if (!chatId) {
      logger.warn(`${LOG_PREFIX} Received chat snapshot but no chat is subscribed`);
      return;
    }

    // Route snapshot updates (replace semantics — prevents cross-chat message leaking)
    useChatStore.getState().processChatStreamUpdates(chatId, updates, true);
  },

  handleChatPaginationInfo: (pagination) => {
    const chatId = get().subscribedChatId;
    if (!chatId) return;

    useChatStore.setState((state) => ({
      messagePagination: {
        ...state.messagePagination,
        [chatId]: {
          ...state.messagePagination[chatId],
          total: pagination.total,
          hasMore: pagination.hasMore,
          oldestOrdinal: pagination.oldestOrdinal,
        },
      },
    }));
  },

  handleChatContextUsage: (contextUsage) => {
    const chatId = get().subscribedChatId;
    if (!chatId) return;

    const existingChatUsage = useChatStore.getState().contextUsage[chatId] || {};
    useChatStore.setState((state) => ({
      contextUsage: {
        ...state.contextUsage,
        [chatId]: {
          ...existingChatUsage,
          [chatId]: {
            threadTokenCount: contextUsage.threadTokenCount,
            compactionThreshold: contextUsage.compactionThreshold,
          },
        },
      },
    }));
  },
}));

// ============================================
// Update Handlers
// ============================================

function parseChatState(state: unknown): ChatState {
  if (typeof state === "number") {
    return state as ChatState;
  }
  if (typeof state !== "string") {
    return ChatState.IDLE;
  }

  switch (state.toLowerCase()) {
    case "idle":
      return ChatState.IDLE;
    case "archived":
      return ChatState.ARCHIVED;
    default:
      return ChatState.IDLE;
  }
}


function handleChatStateChange(update: UserUpdate) {
  const { chat_id } = update;
  if (!chat_id) return;

  const data = update.data as {
    state: string;
    previous_state: string;
    reason: string;
    title?: string;
  };

  logger.debug(`${LOG_PREFIX} Chat state change`, {
    chatId: chat_id.slice(0, 8),
    from: data.previous_state,
    to: data.state,
    reason: data.reason,
  });

  const chatStore = useChatStore.getState();
  const nextState = parseChatState(data.state);
  const previousState = parseChatState(data.previous_state);
  const isBecomingArchived =
    nextState === ChatState.ARCHIVED && previousState !== ChatState.ARCHIVED;
  const isBeingRestored =
    previousState === ChatState.ARCHIVED && nextState !== ChatState.ARCHIVED;

  // Handle archive transition: move chat from active list to archived list
  if (isBecomingArchived) {
    // Chat is being archived — remove its activity entry
    useActivityStore.getState().removeActivity(chat_id);

    // Find the chat in the active list
    const chatToArchive = chatStore.chats.get(chat_id);
    
    if (chatToArchive) {
      // Remove from active chats list (with fresh state)
      useChatStore.setState((state) => {
        const newChats = new Map(state.chats);
        newChats.delete(chat_id);
        return {
          chats: newChats,
          chatOrder: state.chatOrder.filter((cid) => cid !== chat_id),
        };
      });
      
      // Add to archived chats list (optimistic). Resolve worktree_name from worktree store
      // so the sidebar shows the correct name immediately instead of "Unknown Workspace".
      const worktreeName = chatToArchive.worktreeId
        ? useWorktreeStore.getState().worktrees.find(w => w.id === chatToArchive.worktreeId)?.name
        : undefined;
      useChatStore.getState().addArchivedChat({
        ...chatToArchive,
        state: nextState,
        worktreeName: worktreeName,
      });

      // Hydrate archived metadata (worktree_name/worktree_deleted_at) from backend.
      // This is necessary because archived chat metadata is only available via
      // ListArchivedChats (not via the state-change update).
      // Debounced to batch multiple archive events into a single fetch.
      scheduleArchivedChatHydration(chat_id);
      
      logger.info(`${LOG_PREFIX} Chat archived and moved to archived list: ${chat_id.slice(0, 8)}`);
    } else {
      // Chat was already optimistically removed from active list (e.g., by deleteChat).
      // Still need to add to archived list if not already there.
      // Construct a minimal chat object from the update data.
      const existsInArchived = useChatStore.getState().archivedChats.some(
        (c) => c.id === chat_id
      );
      
      if (!existsInArchived) {
        // Resolve worktree_name from worktree store for immediate display
        const worktreeName = update.worktree_id
          ? useWorktreeStore.getState().worktrees.find(w => w.id === update.worktree_id)?.name
          : undefined;
        const minimalChat: Chat = {
          id: chat_id,
          userId: '',
          title: data.title || 'Archived Chat',
          projectId: update.project_id || '',
          worktreeId: update.worktree_id,
          worktreeName: worktreeName,
          state: nextState,
          createdAt: update.created_at,
          updatedAt: update.created_at,
          lastActive: update.created_at,
          selectedPresets: {},
          needsRecovery: false,
          activity: 0,
          unread: false,
        };
        useChatStore.getState().addArchivedChat(minimalChat);
        logger.info(`${LOG_PREFIX} Chat archived (optimistic removal handled): ${chat_id.slice(0, 8)}`);

        // Same hydration logic as above, but for the minimal chat object path.
        // Debounced to batch multiple archive events into a single fetch.
        scheduleArchivedChatHydration(chat_id);
      }
    }
    return; // Don't continue with normal state update
  }

  // Handle restore transition: move chat from archived list to active list
  if (isBeingRestored) {
    // Remove from archived list and get the chat data
    const restoredChat = useChatStore.getState().removeArchivedChat(chat_id);
    
    if (restoredChat) {
      // Add back to active chats list with new state
      useChatStore.setState((state) => {
        const newChats = new Map(state.chats);
        newChats.set(chat_id, { ...restoredChat, state: nextState });
        return {
          chats: newChats,
          chatOrder: [...state.chatOrder, chat_id],
        };
      });
      
      logger.info(`${LOG_PREFIX} Chat restored and moved to active list: ${chat_id.slice(0, 8)}`);
    } else {
      // Chat wasn't in archived list (maybe it was already optimistically restored)
      // Check if it already exists in active list
      const existsInActive = useChatStore.getState().chats.has(chat_id);
      
      if (!existsInActive) {
        // Construct a minimal chat object from the update data
        const minimalChat: Chat = {
          id: chat_id,
          userId: '',
          title: data.title || 'Restored Chat',
          projectId: update.project_id || '',
          worktreeId: update.worktree_id,
          state: nextState,
          createdAt: update.created_at,
          updatedAt: update.created_at,
          lastActive: update.created_at,
          selectedPresets: {},
          needsRecovery: false,
          activity: 0,
          unread: false,
        };
        useChatStore.setState((state) => {
          const newChats = new Map(state.chats);
          newChats.set(chat_id, minimalChat);
          return {
            chats: newChats,
            chatOrder: [...state.chatOrder, chat_id],
          };
        });
        logger.info(`${LOG_PREFIX} Chat restored (optimistic restore handled): ${chat_id.slice(0, 8)}`);
      } else {
        logger.debug(`${LOG_PREFIX} Chat already in active list, skipping restore: ${chat_id.slice(0, 8)}`);
      }
    }
    return; // Don't continue with normal state update
  }

  // Normal state change (not archive/restore): update chat.state in place
  // Activity (workflow running) is tracked separately via activityStore

  // Update chats Map in a single setState with fresh state
  useChatStore.setState((state) => {
    const existing = state.chats.get(chat_id);
    if (!existing) return state;
    const newChats = new Map(state.chats);
    newChats.set(chat_id, { ...existing, state: nextState });
    return { chats: newChats };
  });

  // Handle unread flag from the update payload
  const unread = (data as { unread?: boolean }).unread;
  if (unread !== undefined) {
    // Update the chat's unread field
    useChatStore.setState((state) => {
      const existing = state.chats.get(chat_id);
      if (!existing) return state;
      const newChats = new Map(state.chats);
      newChats.set(chat_id, { ...existing, unread });
      return { chats: newChats };
    });

    // If unread=true and user is viewing this chat, auto-dismiss (fire-and-forget)
    const isViewingThisChat = useChatStore.getState().activeChatId === chat_id;
    const notificationStore = useNotificationStore.getState();
    const shouldNotifyEvenWhenViewing = notificationStore.notifyAlways;

    if (unread && isViewingThisChat && !shouldNotifyEvenWhenViewing) {
      logger.debug(`${LOG_PREFIX} Auto-dismissing unread - user is viewing chat: ${chat_id.slice(0, 8)}`);
      void useChatStore.getState().dismissChat(chat_id);
      return;
    }
  }

  // Show OS notification if unread and user isn't actively viewing this chat
  if (unread === true && (data.reason === 'workflow_completed' || data.reason === 'approval_required')) {
    // Skip notifications for events that happened before the app started
    // This prevents notifications from firing for replayed historical events
    const eventTime = new Date(update.created_at).getTime();
    if (eventTime < appStartTime) {
      logger.debug(`${LOG_PREFIX} Skipping notification for old event: ${chat_id.slice(0, 8)}`, {
        eventTime: new Date(eventTime).toISOString(),
        appStartTime: new Date(appStartTime).toISOString(),
      });
      return;
    }
    
    logger.info(`${LOG_PREFIX} Processing notification request`, {
      chatId: chat_id.slice(0, 8),
      reason: data.reason,
      state: data.state,
    });
    
    // Check if we should show notification (use sync version for reliability)
    // The sync version will work even if store isn't fully initialized
    const currentChatStore = useChatStore.getState();
    const activeChatId = currentChatStore.activeChatId;
    const isViewingThisChat = activeChatId === chat_id;
    
    const notificationStore = useNotificationStore.getState();
    const notifyAlways = notificationStore.notifyAlways;
    const directPermission = getNotificationPermission();

    // Show notification if permission is granted and (not viewing chat OR notifyAlways is enabled)
    if (directPermission === "granted" && (!isViewingThisChat || notifyAlways)) {
      // Get the chat title from the update or from the chats list
      const chat = currentChatStore.chats.get(chat_id);
      const chatTitle = data.title || chat?.title || '';
      
      // Show notification with sound
      const soundOptions = getNotificationSoundOptions();
      const navigateToChat = async (id: string) => {
        try {
          const chatStore = useChatStore.getState();
          
          // Check if we're already viewing this chat - if so, just focus the window, don't navigate
          const currentActiveChatId = chatStore.activeChatId;
          if (currentActiveChatId === id || currentActiveChatId === chat_id) {
            logger.debug(`${LOG_PREFIX} Already viewing this chat, skipping navigation - just focusing window`, {
              chatId: id.slice(0, 8),
            });
            // Just focus the window, no need to navigate
            if (typeof window !== "undefined") {
              window.focus();
            }
            return;
          }
          
          logger.warn(`${LOG_PREFIX} 🚀🚀🚀 NAVIGATING TO CHAT FROM NOTIFICATION 🚀🚀🚀`, {
            chatId: id.slice(0, 8),
            timestamp: new Date().toISOString(),
          });
          
          const projectStore = useProjectStore.getState();
          const worktreeStore = useWorktreeStore.getState();
          const chatNavigationStore = useChatNavigationStore.getState();
          
          logger.warn(`${LOG_PREFIX} Current state`, {
            activeChatId: chatStore.activeChatId?.slice(0, 8),
            chatsCount: chatStore.chats.size,
            currentProject: projectStore.currentProject?.id?.slice(0, 8),
          });
          
          // Find the chat object
          let chat = chatStore.chats.get(id);
          if (!chat) {
            // Try to reload chats in case it's not in the list yet
            await useChatStore.getState().loadChats();
            const updatedState = useChatStore.getState();
            chat = updatedState.chats.get(id);
            if (!chat) {
              logger.error(`${LOG_PREFIX} Chat not found after reload: ${id.slice(0, 8)}`);
              return;
            }
          }
          
          // Switch worktree context if needed
          const currentProject = projectStore.currentProject;
          if (chat.worktreeId && currentProject?.id) {
            const worktrees = worktreeStore.worktrees;
            const worktree = worktrees.find(w => w.id === chat.worktreeId);
            if (worktree) {
              logger.warn(`${LOG_PREFIX} 🔄 Switching worktree context: ${worktree.name}`);
              await worktreeStore.switchWorktreeContext(currentProject.id, worktree);
              logger.warn(`${LOG_PREFIX} ✅ Worktree context switched`);
            }
          } else if (currentProject?.id) {
            logger.warn(`${LOG_PREFIX} 🔄 Switching to main worktree context`);
            await worktreeStore.switchWorktreeContext(currentProject.id, null);
            logger.warn(`${LOG_PREFIX} ✅ Switched to main worktree`);
          }
          
          // Select the chat - ALWAYS force navigation, even if already selected
          logger.warn(`${LOG_PREFIX} 🎯 SELECTING CHAT (FORCED): ${chat.id.slice(0, 8)}`);
          
          // Now select the chat - this triggers full navigation logic
          logger.warn(`${LOG_PREFIX} Calling selectChat for: ${chat.id.slice(0, 8)}`);
          useChatStore.getState().selectChat(chat);
          
          // Add to navigation queue
          chatNavigationStore.navigateToChat(chat.id);
          
          // Force a state refresh by reading and setting again
          const stateAfter = useChatStore.getState();
          if (stateAfter.activeChatId !== chat.id) {
            logger.error(`${LOG_PREFIX} ❌ Active chat not set correctly! Setting directly...`);
            useChatStore.getState().setActiveChat(chat.id);
          }
          
          // Verify it worked
          const finalActiveChatId = useChatStore.getState().activeChatId;
          logger.warn(`${LOG_PREFIX} ✅✅✅ NAVIGATION COMPLETE ✅✅✅`, {
            targetChatId: id.slice(0, 8),
            finalActiveChatId: finalActiveChatId?.slice(0, 8),
            success: finalActiveChatId === id || finalActiveChatId === chat.id,
          });
          
          // Force window focus one more time after navigation
          if (typeof window !== "undefined") {
            setTimeout(() => {
              window.focus();
            }, 100);
          }
        } catch (error) {
          logger.error(`${LOG_PREFIX} ❌❌❌ ERROR NAVIGATING TO CHAT ❌❌❌`, {
            chatId: id.slice(0, 8),
            error,
            stack: error instanceof Error ? error.stack : undefined,
          });
        }
      };
      
      if (data.reason === 'approval_required') {
        showApprovalRequiredNotification(chat_id, chatTitle, navigateToChat, soundOptions);
      } else {
        showWorkflowCompletionNotification(chat_id, chatTitle, navigateToChat, soundOptions);
      }
      
      logger.info(`${LOG_PREFIX} Notification shown for chat: ${chat_id.slice(0, 8)} (${data.reason})`);
    } else {
      logger.debug(`${LOG_PREFIX} Skipping notification (viewing chat or disabled): ${chat_id.slice(0, 8)}`);
    }
  }
}

function handleChatConfigChanged(update: UserUpdate) {
  const { chat_id } = update;
  if (!chat_id) return;

  // NOTE: model, temperature, max_tokens, agent, auto_approve are workflow params now
  const data = update.data as {
    workflow_name?: string | null;
    state?: string;
    title?: string;
    updated_at: string;
  };

  logger.debug(`${LOG_PREFIX} Chat config changed`, {
    chatId: chat_id.slice(0, 8),
    workflow_name: data.workflow_name,
  });

  // Build partial update - only include fields that are defined
  const partialUpdate: Partial<Chat> = {
    updatedAt: data.updated_at,
  };
  if (data.workflow_name !== undefined) partialUpdate.workflowName = data.workflow_name ?? undefined;
  if (data.state !== undefined) partialUpdate.state = parseChatState(data.state);
  if (data.title !== undefined) partialUpdate.title = data.title;

  // Update chats Map in a single setState
  useChatStore.setState((state) => {
    const existing = state.chats.get(chat_id);
    if (!existing) return state;
    const newChats = new Map(state.chats);
    newChats.set(chat_id, { ...existing, ...partialUpdate });
    return { chats: newChats };
  });
}

function handleChatCreated(update: UserUpdate) {
  const data = update.data as {
    chat_id: string;
    title: string;
    project_id?: string;
    worktree_id?: string;
  };

  logger.debug(`${LOG_PREFIX} New chat created`, {
    chatId: data.chat_id?.slice(0, 8),
    title: data.title,
  });

  const chatStore = useChatStore.getState();
  
  // Check if the chat already exists in the list (added by createChat optimistically)
  // This prevents duplicates when the chat_created event arrives after/during createChat
  const chatExists = chatStore.chats.has(data.chat_id);
  
  if (chatExists) {
    logger.debug(`${LOG_PREFIX} Chat already exists in store, skipping reload`, {
      chatId: data.chat_id?.slice(0, 8),
    });
    return;
  }
  
  // Only reload if the chat doesn't exist - this handles the case where
  // the chat was created from another browser window/tab
  logger.debug(`${LOG_PREFIX} Chat not in store, reloading chats`, {
    chatId: data.chat_id?.slice(0, 8),
  });
  chatStore.loadChats();
}

function handleChatTitleChanged(update: UserUpdate) {
  const { chat_id } = update;
  if (!chat_id) return;

  const data = update.data as {
    title: string;
    previous_title?: string;
  };

  logger.debug(`${LOG_PREFIX} Chat title changed`, {
    chatId: chat_id.slice(0, 8),
    title: data.title,
  });

  // Update chats Map in a single setState
  useChatStore.setState((state) => {
    const existing = state.chats.get(chat_id);
    if (!existing) return state;
    const newChats = new Map(state.chats);
    newChats.set(chat_id, { ...existing, title: data.title });
    return { chats: newChats };
  });
}

function handleChatDeleted(update: UserUpdate) {
  const { chat_id } = update;
  if (!chat_id) return;

  logger.debug(`${LOG_PREFIX} Chat deleted`, {
    chatId: chat_id.slice(0, 8),
  });

  // Remove from active chats list in a single setState with fresh state
  useChatStore.setState((state) => {
    const newChats = new Map(state.chats);
    newChats.delete(chat_id);
    return {
      chats: newChats,
      chatOrder: state.chatOrder.filter((cid) => cid !== chat_id),
    };
  });

  // Remove from archived chats list (in case it was archived)
  useChatStore.getState().removeArchivedChat(chat_id);

  // Remove stale activity entry
  useActivityStore.getState().removeActivity(chat_id);
}

// ============================================
// Background Process Handlers
// ============================================

function handleChatActivityChanged(update: UserUpdate) {
  const { chat_id } = update;
  if (!chat_id) return;

  const data = update.data as { chat_id: string; activity: number; timestamp: string };

  const activity = (data.activity ?? 0) as ChatActivity;

  logger.info(`${LOG_PREFIX} Chat activity changed`, {
    chatId: chat_id.slice(0, 8),
    activity,
  });

  // Single source of truth: update the activity store
  useActivityStore.getState().setActivity(chat_id, activity);

  // Clear pending approvals when activity goes idle.
  // NOTE: Thread activity is intentionally NOT cleared here. Thread metadata
  // (thread_title, router_decision, spawned_by_node_id) must persist across
  // pause/resume so the timeline can display thread names and routing decisions.
  // The useIsThreadActive hook returns false when the chat is not RUNNING,
  // so threads won't appear as active. Thread data is cleared in
  // cleanupChatState/evictChatData when the chat is fully torn down.
  if (activity === ChatActivity.IDLE) {
    useChatStore.setState((state) => ({
      pendingApprovals: {
        ...state.pendingApprovals,
        [chat_id]: [],
      },
    }));
  }
}

function handleProcessStarted(update: UserUpdate) {
  const data = update.data as {
    process_id: string;
    command: string;
    working_dir: string;
    status: string;
    start_time: string;
    session_id?: string;
    worktree_id?: string;
    chat_id?: string;
    ports?: Array<{ port: number; protocol: string; state: string; address: string }>;
  };

  logger.debug(`${LOG_PREFIX} Process started`, {
    processId: data.process_id.slice(0, 8),
    command: data.command.slice(0, 50),
    worktreeId: data.worktree_id || update.worktree_id,
  });

  // Create a new BackgroundProcess with full data from the event
  const newProcess: BackgroundProcess = {
    id: data.process_id,
    command: data.command,
    status: BackgroundProcessStatus.RUNNING,
    start_time: data.start_time,
    working_dir: data.working_dir,
    session_id: data.session_id || "",
    worktree_id: data.worktree_id || update.worktree_id,
    chat_id: data.chat_id || update.chat_id,
    ports: data.ports,
  };

  const processStore = useProcessStore.getState();
  useProcessStore.setState({
    processes: [...processStore.processes.filter(p => p.id !== data.process_id), newProcess],
  });

  // Also update package commands store if it's a worktree-associated process
  const worktreeId = data.worktree_id || update.worktree_id;
  if (worktreeId) {
    usePackageCommandsStore.getState().handleProcessStarted({
      id: data.process_id,
      command: data.command,
      worktree_id: worktreeId,
      working_dir: data.working_dir,
      start_time: data.start_time,
    });
  }
}

function handleProcessCompleted(update: UserUpdate) {
  const data = update.data as {
    process_id: string;
    command: string;
    working_dir: string;
    status: string;
    exit_code?: number;
    start_time: string;
    end_time?: string;
    session_id?: string;
    worktree_id?: string;
    chat_id?: string;
    ports?: Array<{ port: number; protocol: string; state: string; address: string }>;
  };

  logger.info(`${LOG_PREFIX} Process completed`, {
    processId: data.process_id.slice(0, 8),
    exitCode: data.exit_code,
    command: data.command?.slice(0, 30),
  });

  updateProcessStatus(update, data, BackgroundProcessStatus.COMPLETED);
}

function handleProcessFailed(update: UserUpdate) {
  const data = update.data as {
    process_id: string;
    command: string;
    working_dir: string;
    status: string;
    exit_code?: number;
    start_time: string;
    end_time?: string;
    session_id?: string;
    worktree_id?: string;
    chat_id?: string;
    ports?: Array<{ port: number; protocol: string; state: string; address: string }>;
  };

  logger.info(`${LOG_PREFIX} Process failed`, {
    processId: data.process_id.slice(0, 8),
    status: data.status,
    exitCode: data.exit_code,
    command: data.command?.slice(0, 30),
  });

  // Backend sends "killed" status as a "failed" event
  const status =
    data.status === "killed"
      ? BackgroundProcessStatus.KILLED
      : BackgroundProcessStatus.FAILED;
  updateProcessStatus(update, data, status);
}

function handleProcessPortChanged(update: UserUpdate) {
  const data = update.data as {
    process_id: string;
    ports?: Array<{ port: number; protocol: string; state: string; address: string }>;
  };

  logger.info(`${LOG_PREFIX} Process port changed`, {
    processId: data.process_id.slice(0, 8),
    portCount: data.ports?.length || 0,
  });

  const procStore = useProcessStore.getState();
  useProcessStore.setState({
    processes: procStore.processes.map((p) =>
      p.id === data.process_id
        ? { ...p, ports: data.ports }
        : p
    ),
  });
}

function updateProcessStatus(
  update: UserUpdate,
  data: {
    process_id: string;
    command: string;
    working_dir: string;
    status: string;
    exit_code?: number;
    start_time: string;
    end_time?: string;
    session_id?: string;
    worktree_id?: string;
    chat_id?: string;
    ports?: Array<{ port: number; protocol: string; state: string; address: string }>;
  },
  status: BackgroundProcess["status"]
) {
  // Build the full process object for upsert
  const fullProcess: BackgroundProcess = {
    id: data.process_id,
    command: data.command,
    status,
    start_time: data.start_time,
    end_time: data.end_time,
    exit_code: data.exit_code,
    working_dir: data.working_dir,
    session_id: data.session_id || "",
    worktree_id: data.worktree_id || update.worktree_id,
    chat_id: data.chat_id || update.chat_id,
    ports: data.ports,
  };

  // Update processStore - upsert pattern
  const procStore = useProcessStore.getState();
  const procExists = procStore.processes.some((p) => p.id === data.process_id);
  logger.info(`${LOG_PREFIX} Updating processStore`, {
    processId: data.process_id.slice(0, 8),
    status,
    exists: procExists,
    storeCount: procStore.processes.length,
  });
  if (procExists) {
    useProcessStore.setState({
      processes: procStore.processes.map((p) =>
        p.id === data.process_id
          ? {
              ...p,
              status,
              exit_code: data.exit_code,
              end_time: data.end_time,
              ports: data.ports || p.ports,
            }
          : p
      ),
    });
  } else {
    // Process doesn't exist - add it
    useProcessStore.setState({
      processes: [fullProcess, ...procStore.processes],
    });
  }

  // Update package commands store - it has its own upsert logic
  const pkgStore = usePackageCommandsStore.getState();
  const pkgExists = pkgStore.processes.some((p) => p.id === data.process_id);
  logger.info(`${LOG_PREFIX} Updating packageCommandsStore`, {
    processId: data.process_id.slice(0, 8),
    status,
    exists: pkgExists,
    storeCount: pkgStore.processes.length,
  });
  if (status === BackgroundProcessStatus.COMPLETED) {
    pkgStore.handleProcessCompleted(data.process_id, data.exit_code, data.end_time, {
      command: data.command,
      working_dir: data.working_dir,
      start_time: data.start_time,
      worktree_id: data.worktree_id || update.worktree_id,
    });
  } else {
    pkgStore.handleProcessFailed(data.process_id, data.exit_code, data.end_time, {
      command: data.command,
      working_dir: data.working_dir,
      start_time: data.start_time,
      worktree_id: data.worktree_id || update.worktree_id,
    });
  }
}

/**
 * Handle refetch events from the user stream.
 * Routes to the refetchStore which notifies subscribed components.
 */
function handleRefetch(update: UserUpdate) {
  const refetchType = update.data?.type as RefetchType | undefined;
  if (!refetchType) {
    logger.warn(`${LOG_PREFIX} Received refetch event with no type`, { data: update.data });
    return;
  }
  triggerRefetch(refetchType, update.entity_id);
}

// Break the circular dependency: chatStore needs to call globalUpdatesStore
// methods, but can't import it directly. This registers the reference.
initGlobalUpdatesStoreRef({ useGlobalUpdatesStore } as typeof import("./globalUpdatesStore"));