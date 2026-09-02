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
import type { UserUpdate, ChatUpdate, ConnectionStatus, ContextUsageInfo, MessagePaginationInfo } from "../types/streaming";
import { useChatStore, initGlobalUpdatesStoreRef } from "./chatStore";
import { ChatState } from "../gen/reliant/v1/chat_pb";
import type { Chat } from "../types/chat";
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
import { getEventBus } from "../lib/events";
import { queryClient } from "../lib/query-client";
import { chatKeys, patchChatCaches, removeChatFromListCache, getChatFromCache } from "../hooks/chat-queries";
import { setMessagesMetaInCache } from "../hooks/message-queries";
import { approvalKeys } from "../hooks/approval-queries";
import { showWorkflowCompletionNotification, showApprovalRequiredNotification, getNotificationPermission } from "../lib/notifications";
import { getNotificationSoundOptions, useNotificationStore } from "./notificationStore";
import { triggerRefetch, type RefetchType } from "./refetchStore";
import { setDaemonLastSeen } from "../api/grpc-client";
import { toast } from "../lib/toast-manager";

const LOG_PREFIX = "[🌐 GlobalUpdates]";

/**
 * Republish the drain announcements in a chat-update batch onto the event bus.
 *
 * The mailbox strip is fed by a poll, so before this signal existed the only
 * thing that retired a drained row was the next poll happening to come back
 * without it — leaving the message rendered in the transcript AND in the strip
 * for up to a poll interval. The announcement rides the same ordered channel
 * as the messages it describes, so publishing it right after those messages
 * are committed closes that window rather than shrinking it.
 */
function publishDrainedMailboxRows(chatId: string, updates: ChatUpdate[]): void {
  for (const update of updates) {
    if (update.update_type !== "agent_messages_drained") continue;
    if (!update.message_ids?.length) continue;

    getEventBus().emit("agentMailbox:drained", {
      chatId,
      thread: update.thread,
      messageIds: update.message_ids,
    });
  }
}

// Timestamp when the app started - only notify for events after this
const appStartTime = Date.now();

// Update IDs that already produced an OS notification. Gap-resyncs replay
// recent updates from the DB (idempotent for state patching), so
// notifications need their own dedup to not fire twice.
const notifiedUpdateIds = new Set<string>();
const NOTIFIED_UPDATE_IDS_MAX = 500;

// Set when the stream errors or ends; the next successful connect then
// refreshes list state as a safety net (the reconnect's DB replay restores
// update-driven state, but ephemeral signals like REFETCH are not replayed).
let streamWasDisrupted = false;

/**
 * THE single write path for chat metadata updates arriving over the stream:
 * patches the React Query list/detail caches (the single source of truth for
 * Chat objects). Never fabricates entries — unknown chats are left to a list
 * refetch.
 */
function applyChatPatch(
  projectId: string | undefined,
  chatId: string,
  patch: Partial<Chat>
): void {
  patchChatCaches(projectId, chatId, patch);
}

interface GlobalUpdatesState {
  // Connection state
  connectionStatus: ConnectionStatus;
  lastSequence: number;
  
  // Last known daemon heartbeat (unix seconds). Set via DAEMON_HEARTBEAT events.
  daemonLastSeen: number | null;

  // Detected listener ports per daemon (from heartbeat detected_ports —
  // loopback/wildcard LISTEN sockets inside a cloud workspace). Drives the
  // "Open preview" affordance next to the daemon status dot.
  daemonDetectedPorts: Record<string, number[]>;

  // Currently subscribed chat ID for detail events
  subscribedChatId: string | null;
  
  // Unified gRPC streaming service
  wsService: UserStreamingService | null;
  
  // Actions
  setLastSequence: (seq: number) => void;
  connect: () => void;
  disconnect: () => void;
  subscribeToChatDetails: (chatId: string) => void;
  // Pass the chat the caller subscribed to. The call is ignored unless that
  // chat is still the subscribed one, so a late effect cleanup cannot tear
  // down a subscription that now belongs to a different chat.
  unsubscribeFromChatDetails: (chatId?: string) => void;
  // Self-healing invariant: the RENDERED chat is the source of truth for
  // what should be subscribed. Callers should invoke this on every render of
  // the active chat (and whenever connection state changes), not just once
  // at selection time — a subscription lost to another component stealing
  // the single slot otherwise never gets reclaimed. No-op for a null id;
  // idempotent when the rendered chat already matches the subscription
  // (delegates to subscribeToChatDetails's own service-truth guard).
  reconcileChatSubscription: (renderedChatId: string | null) => void;
  
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
  daemonDetectedPorts: {},
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
    // Trust the service, not this store's flag. subscribedChatId is set
    // optimistically below, before the connection is known to have
    // succeeded, so on its own it cannot distinguish "subscribed" from
    // "tried to subscribe and failed" — and treating the latter as the
    // former would make every retry a no-op.
    const serviceHasChat =
      state.wsService?.getSubscribedChatId() === chatId &&
      state.wsService?.isConnected();
    if (state.subscribedChatId === chatId && serviceHasChat) {
      logger.debug(`${LOG_PREFIX} Already subscribed to chat`, { chatId: chatId.slice(0, 8) });
      return;
    }

    // Subscription is missing or stale — (re)assert it against the service.
    if (state.wsService) {
      logger.info(`${LOG_PREFIX} Asserting chat subscription`, {
        chatId: chatId.slice(0, 8),
        serviceChatId: state.wsService.getSubscribedChatId()?.slice(0, 8),
        connected: state.wsService.isConnected(),
      });
      set({ subscribedChatId: chatId });
      state.wsService.subscribeToChatDetails(chatId);
      return;
    }

    logger.info(`${LOG_PREFIX} Subscribing to chat detail events`, {
      chatId: chatId.slice(0, 8),
      previousChatId: state.subscribedChatId?.slice(0, 8),
    });

    // No service yet. Record the intent and connect; connect() forwards
    // subscribedChatId into start(), so the subscription rides the initial
    // connection. connect() may defer if chats have not loaded, in which
    // case the ModernApp effect connects once they have and picks this up.
    set({ subscribedChatId: chatId });
    get().connect();
  },

  unsubscribeFromChatDetails: (chatId?: string) => {
    const state = get();
    if (!state.subscribedChatId) return;

    // Ownership check. React effect cleanups run after the next effect body
    // has already resubscribed, and components unmount while the chat they
    // referenced is still on screen. Without this, such a cleanup silently
    // unsubscribes the chat the user is actively viewing: the stream stays
    // connected and heartbeating (so the liveness watchdog never fires) but
    // no chat events arrive, and reselecting the same chat is a no-op
    // because selectChat early-returns on an unchanged activeChatId. The
    // only escape is opening a different chat.
    if (chatId !== undefined && chatId !== state.subscribedChatId) {
      logger.debug(`${LOG_PREFIX} Ignoring stale unsubscribe`, {
        requested: chatId.slice(0, 8),
        subscribed: state.subscribedChatId.slice(0, 8),
      });
      return;
    }

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

  reconcileChatSubscription: (renderedChatId: string | null) => {
    if (!renderedChatId) return;

    const state = get();
    const serviceChatId = state.wsService?.getSubscribedChatId();
    const isReconciled =
      state.subscribedChatId === renderedChatId &&
      serviceChatId === renderedChatId &&
      state.wsService?.isConnected();
    if (isReconciled) return;

    // Mismatch between what's rendered and what's subscribed — this is
    // exactly the "stuck chat" bug: some other component (or a reconnect
    // that forwarded a stale id) stole the single subscription slot and
    // nothing ever re-asserted it. Logged at INFO because that mismatch is
    // otherwise invisible until a user reports missing messages.
    logger.info(`${LOG_PREFIX} Reconciling chat subscription`, {
      renderedChatId: renderedChatId.slice(0, 8),
      subscribedChatId: state.subscribedChatId?.slice(0, 8) ?? null,
      serviceChatId: serviceChatId?.slice(0, 8) ?? null,
    });
    get().subscribeToChatDetails(renderedChatId);
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
        case UserUpdateType.NOTIFICATION:
          handleNotification(update);
          break;
        case UserUpdateType.DAEMON_HEARTBEAT: {
          const data = typeof update.data === "string" ? JSON.parse(update.data) : update.data;
          if (data?.last_heartbeat) {
            const ts = data.last_heartbeat as number;
            set({ daemonLastSeen: ts });
            setDaemonLastSeen(ts);
          }
          // detected_ports is present (possibly empty) on real heartbeats and
          // absent on synthetic connection events — only update when carried,
          // so a reconnect blip doesn't clear a still-valid port set.
          if (data?.daemon_id && Array.isArray(data.detected_ports)) {
            const daemonId = data.daemon_id as string;
            const ports = (data.detected_ports as number[]).filter((p) => Number.isFinite(p));
            set((state) => {
              const prev = state.daemonDetectedPorts[daemonId];
              if (prev && prev.length === ports.length && prev.every((p, i) => p === ports[i])) {
                return state; // unchanged — avoid re-render churn every 15s
              }
              return { daemonDetectedPorts: { ...state.daemonDetectedPorts, [daemonId]: ports } };
            });
          }
          try { getEventBus().emit("daemon:heartbeat"); } catch { /* bus not ready */ }
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

    if (status === "error" || status === "disconnected") {
      streamWasDisrupted = true;
    }

    // After a disruption, refresh chat state once reconnected. The stream's
    // own DB replay (since_seq catch-up) restores update-driven state; this
    // covers ephemeral signals (REFETCH events are deliberately not replayed).
    // NOTE: the old condition (`prevStatus !== "connecting"`) could never
    // fire — every path to "connected" goes through "connecting".
    if (status === "connected" && streamWasDisrupted) {
      streamWasDisrupted = false;
      logger.info(`${LOG_PREFIX} Reconnected after disruption - refreshing chat state`);
      useChatStore.getState().loadChats().catch((err) => {
        logger.warn(`${LOG_PREFIX} Failed to refresh chats on reconnect`, { error: err });
      });
      try { queryClient.invalidateQueries({ queryKey: chatKeys.all }); } catch { /* bus not ready */ }
    }
  },

  handleSync: (lastSequence) => {
    // Informational only. The resume cursor must track PROCESSED updates:
    // the server sends sync before replaying catch-up, so adopting its value
    // here could skip updates if the stream drops mid-replay. lastSequence
    // is advanced by handleUpdate (per update) and seeded by loadChats.
    logger.info(`${LOG_PREFIX} Sync received, server lastSequence: ${lastSequence}`);
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

    // Then announce the mailbox rows this batch drained. AFTER the messages
    // are committed, and in the same synchronous task: the two land in one
    // React commit, so the strip never renders empty against a transcript
    // that has not shown the message yet. The reverse order would open
    // exactly that gap.
    publishDrainedMailboxRows(chatId, updates);
  },

  handleChatSnapshot: (updates) => {
    const chatId = get().subscribedChatId;
    if (!chatId) {
      logger.warn(`${LOG_PREFIX} Received chat snapshot but no chat is subscribed`);
      return;
    }

    // Route snapshot updates (replace semantics — prevents cross-chat message leaking)
    useChatStore.getState().processChatStreamUpdates(chatId, updates, true);

    // A reconnect replays the drain announcements alongside the messages they
    // describe. Publishing them here too means a client that was offline
    // through a drain still retires those rows on the way back, instead of
    // showing them until the next poll.
    publishDrainedMailboxRows(chatId, updates);
  },



  // Pagination info rides alongside the chat snapshot. The snapshot is BOUNDED
  // to the newest N messages, so these three numbers are the only thing that
  // tells the UI a long chat is truncated and where to resume paging from.
  // They are stored ON the message envelope (not in Zustand) so they stay
  // adjacent to the messages they describe and survive the stream's
  // message-only patches — see message-queries.ts.
  handleChatPaginationInfo: (pagination) => {
    const chatId = get().subscribedChatId;
    if (!chatId) return;

    logger.debug(`${LOG_PREFIX} Chat pagination info`, {
      chatId: chatId.slice(0, 8),
      total: pagination.total,
      hasMore: pagination.hasMore,
      oldestSeq: pagination.oldestSeq,
    });

    setMessagesMetaInCache(chatId, {
      total: pagination.total,
      hasMore: pagination.hasMore,
      oldestSeq: pagination.oldestSeq,
    });
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

  const projectId =
    update.project_id || useProjectStore.getState().currentProject?.id;

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

  const nextState = parseChatState(data.state);
  const previousState = parseChatState(data.previous_state);
  const isBecomingArchived =
    nextState === ChatState.ARCHIVED && previousState !== ChatState.ARCHIVED;
  const isBeingRestored =
    previousState === ChatState.ARCHIVED && nextState !== ChatState.ARCHIVED;

  // Handle archive transition
  if (isBecomingArchived) {
    useActivityStore.getState().removeActivity(chat_id);
    logger.info(`${LOG_PREFIX} Chat archived: ${chat_id.slice(0, 8)}`);
    applyChatPatch(projectId, chat_id, { state: nextState });
    // The archived list needs a refetch — we can't construct an ArchivedChat row.
    queryClient.invalidateQueries({ queryKey: chatKeys.archived() });

    // Archiving the chat the user is currently viewing has to move them off it:
    // it just left the sidebar, and the eviction below drops the messages the
    // open ChatInterface is rendering. Falling back to the new-chat view (rather
    // than the next queued chat) keeps this identical to archiving a workspace.
    const chatStore = useChatStore.getState();
    if (chatStore.activeChatId === chat_id) {
      const archivedChat = getChatFromCache(chat_id);
      chatStore.clearCurrentChat(
        update.worktree_id ?? archivedChat?.worktreeId ?? null
      );
      void useChatNavigationStore.getState().removeFromQueue(chat_id);
    }

    // Release the chat's retained memory (messages cache is gcTime: Infinity).
    // Safe on archive: restoring re-seeds via the useMessages queryFn plus the
    // fresh stream snapshot on re-subscribe.
    chatStore.evictChat(chat_id);
    return;
  }

  // Handle restore transition
  if (isBeingRestored) {
    logger.info(`${LOG_PREFIX} Chat restored: ${chat_id.slice(0, 8)}`);
    applyChatPatch(projectId, chat_id, { state: nextState });
    queryClient.invalidateQueries({ queryKey: chatKeys.archived() });
    return;
  }

  // Normal state change (not archive/restore): update chat state (and the
  // unread flag when present) in place. Activity (workflow running) is
  // tracked separately via activityStore.
  const unread = (data as { unread?: boolean }).unread;
  applyChatPatch(projectId, chat_id, {
    state: nextState,
    ...(unread !== undefined ? { unread } : {}),
  });

  if (unread !== undefined) {
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

    // Replay dedup: gap-resyncs re-deliver recent updates from the DB. State
    // patching is idempotent, but a notification must fire at most once.
    if (update.id) {
      if (notifiedUpdateIds.has(update.id)) {
        logger.debug(`${LOG_PREFIX} Skipping duplicate notification (replayed update)`, {
          updateId: update.id,
        });
        return;
      }
      if (notifiedUpdateIds.size >= NOTIFIED_UPDATE_IDS_MAX) {
        notifiedUpdateIds.clear();
      }
      notifiedUpdateIds.add(update.id);
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
      // Get the chat title from the update or from the chat cache
      const chat = getChatFromCache(chat_id);
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
            currentProject: projectStore.currentProject?.id?.slice(0, 8),
          });
          
          // Find the chat object in the React Query cache
          let chat = getChatFromCache(id);
          if (!chat) {
            // Try to reload chats in case it's not in the list yet
            await useChatStore.getState().loadChats();
            chat = getChatFromCache(id);
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

  const projectId =
    update.project_id || useProjectStore.getState().currentProject?.id;

  const patch: Partial<Chat> = {};
  if (data.workflow_name !== undefined) {
    patch.workflowName = data.workflow_name ?? undefined;
  }
  if (data.title !== undefined) {
    patch.title = data.title;
  }
  if (data.state !== undefined) {
    patch.state = parseChatState(data.state);
  }
  // applyChatPatch also updates the Zustand map — ChatInput preset restore
  // reads workflowName/selectedPresets from the Zustand map, not React Query.
  applyChatPatch(projectId, chat_id, patch);
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

  // The event payload is partial (chat_id/title/project_id/worktree_id only);
  // upserting a fabricated Chat row would render a broken sidebar entry, so
  // refetch the list instead.
  queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
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

  const projectId =
    update.project_id || useProjectStore.getState().currentProject?.id;
  applyChatPatch(projectId, chat_id, { title: data.title });
}

function handleChatDeleted(update: UserUpdate) {
  const { chat_id } = update;
  if (!chat_id) return;

  logger.debug(`${LOG_PREFIX} Chat deleted`, {
    chatId: chat_id.slice(0, 8),
  });

  // Remove stale activity entry
  useActivityStore.getState().removeActivity(chat_id);

  const projectId =
    update.project_id || useProjectStore.getState().currentProject?.id;
  removeChatFromListCache(projectId, chat_id);
  // Deletes can come from the archived list too — refetch it.
  queryClient.invalidateQueries({ queryKey: chatKeys.archived() });
  // Release the chat's retained memory (messages cache is gcTime: Infinity,
  // per-chat Zustand slices have no TTL).
  useChatStore.getState().evictChat(chat_id);
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
    seq: update.sequence_number,
  });

  // Single source of truth: update the activity store. The event's sequence
  // number decides precedence against list/get snapshot writes — no clocks.
  useActivityStore
    .getState()
    .applyStreamActivity(chat_id, activity, update.sequence_number);

  // Clear pending approvals when activity goes idle.
  // NOTE: Thread activity is intentionally NOT cleared here. Thread metadata
  // (thread_title, router_decision, spawned_by_node_id) must persist across
  // pause/resume so the timeline can display thread names and routing decisions.
  // The useIsThreadActive hook returns false when the chat is not RUNNING,
  // so threads won't appear as active.
  if (activity === ChatActivity.IDLE) {
    // Approvals are server data in the React Query cache. On IDLE we don't hold
    // the resolved approval objects, so reconcile from the server (a genuine
    // "something changed, refetch" nudge — the one case invalidate is correct).
    queryClient.invalidateQueries({ queryKey: approvalKeys.list(chat_id) });

    // IDLE is the authoritative "nothing is streaming" signal. Streaming
    // deltas race message finalization on a separate server channel, so a
    // stale tail can rebuild a placeholder AFTER the final message arrived —
    // at the end of a run nothing else would ever clean it up.
    useChatStore.getState().clearStreamingState(chat_id);
  }

  // Clear discuss mode when workflow resumes (activity becomes RUNNING)
  if (activity === ChatActivity.RUNNING) {
    useChatStore.setState((state) => {
      if (!state.discussMode[chat_id]) return state;
      return {
        discussMode: { ...state.discussMode, [chat_id]: false },
      };
    });
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

  try { getEventBus().emit("process:updated", { processId: data.process_id, status: "running" }); } catch {}
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

  try { getEventBus().emit("process:updated", { processId: data.process_id, status: "completed" }); } catch {}
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

  try { getEventBus().emit("process:updated", { processId: data.process_id, status: data.status === "killed" ? "killed" : "failed" }); } catch {}
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

  try { getEventBus().emit("process:updated", { processId: data.process_id, status: "port_changed" }); } catch {}
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
 * Handle USER_UPDATE_TYPE_NOTIFICATION events from the user stream.
 *
 * Today this only carries reason: "daemon_command_failed" — the failure
 * counterpart of the file_tree refetch a successful git.clone emits. A
 * clone dispatched via the pending-command queue (control-plane's CloneRepo
 * enqueues and returns immediately; see gitcredential.svc.Clone) has no
 * RPC response to fail on the client's original call, so this row —
 * replayed on reconnect just like any other user_update — is the only
 * place a background clone failure becomes visible at all.
 */
function handleNotification(update: UserUpdate) {
  const data = update.data as { reason?: string; command_type?: string; error?: string } | undefined;
  if (data?.reason === "daemon_command_failed") {
    const label = data.command_type === "git.clone" ? "Clone" : (data.command_type ?? "Command");
    toast.error(`${label} failed: ${data.error ?? "unknown error"}`);
    return;
  }
  logger.debug(`${LOG_PREFIX} Unhandled notification reason`, { data });
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
  // Keep existing refetchStore call for backwards compat
  triggerRefetch(refetchType, update.entity_id);

  // Also emit via event bus
  try {
    const eventMap: Record<string, keyof import("../lib/events").EventMap> = {
      worktree_changes: "refetch:worktreeChanges",
      workflow_executions: "refetch:workflowExecutions",
      config_health: "refetch:configHealth",
      plan_tasks: "refetch:planTasks",
      file_tree: "refetch:fileTree",
    };
    const eventName = eventMap[refetchType];
    if (eventName) {
      getEventBus().emit(eventName, { entityId: update.entity_id });
    }
  } catch {}
}

// Break the circular dependency: chatStore needs to call globalUpdatesStore
// methods, but can't import it directly. This registers the reference.
initGlobalUpdatesStoreRef({ useGlobalUpdatesStore } as typeof import("./globalUpdatesStore"));