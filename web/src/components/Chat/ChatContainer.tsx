/**
 * ChatContainer - State management for tab-based chats
 *
 * Subscribes to stores and manages state for a specific tab.
 * Passes all data as props to ChatPresenter for rendering.
 */

import { useMemo, useCallback } from "react";
import { MessageRole } from "../../gen/reliant/v1/chat_pb";
import { ChatPresenter } from "./ChatPresenter";
import {
  useChat,
  useChatMessages,
  useChatApprovals,
  useErrorEvents,
  useInfoEvents,
  useRunOutputs,
  usePendingApprovals,
  usePendingYield,
  useStreamingMessages,
} from "../../store/chatStoreHooks";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { useIsChatRunning } from "../../store/activityStore";
import { useChatCurrentActivity } from "../../store/threadActivityStore";
import { useChatStore } from "../../store/chatStore";
import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { transformWorkflowExecution } from "./ExecutionSidebar";
import * as Sentry from "@sentry/react";
import { logger } from "../../lib/logger";
import { toast } from "../../lib/toast-manager";

// Stable empty references
const EMPTY_ARRAY: never[] = [];

interface ChatContainerProps {
  tabId: string;
  isFocused?: boolean;
}

export function ChatContainer({ tabId, isFocused = true }: ChatContainerProps) {
  // In the new navigation system, tabId IS the chatId (no separate tabs)
  // We keep the tabId prop name for compatibility but treat it as chatId
  const chatId = tabId;

  // Get worktree and project IDs
  const currentWorktreeId = useWorktreeStore(
    (state) => state.currentWorktree?.id || null,
  );
  const currentProjectId = useProjectStore(
    (state) => state.currentProject?.id || null,
  );

  // Subscribe to chat-specific data using optimized hooks
  const storeMessages = useChatMessages(chatId);
  const streamingMessages = useStreamingMessages(chatId);

  // FIX 2: Merge streaming messages with store messages
  // This ensures buffered streaming content is immediately visible
  // Streaming messages are keyed by thread, so we merge by ID
  const messages = useMemo(() => {
    if (streamingMessages.length === 0) return storeMessages;

    const msgs = [...storeMessages];
    for (const streamingMsg of streamingMessages) {
      const existingIdx = msgs.findIndex((m) => m.id === streamingMsg.id);
      if (existingIdx >= 0) {
        // Update existing streaming message in place
        msgs[existingIdx] = streamingMsg;
      } else {
        // Add new streaming message
        msgs.push(streamingMsg);
      }
    }
    return msgs;
  }, [storeMessages, streamingMessages]);

  const approvals = useChatApprovals(chatId);
  const errorEvents = useErrorEvents(chatId);
  const infoEvents = useInfoEvents(chatId);
  const runOutputs = useRunOutputs(chatId);
  const currentChat = useChat(chatId);
  const connectionStatus = useGlobalUpdatesStore((s) => s.connectionStatus);
  const isChatBusy = useIsChatRunning(chatId);
  const pendingApprovals = usePendingApprovals(chatId);
  const pendingYield = usePendingYield(chatId);
  const hasPendingYield = !!pendingYield;
  const currentActivity = useChatCurrentActivity(chatId);

  // Process messages: sort by ordinal and filter out agent messages
  const processedMessages = useMemo(() => {
    if (messages.length === 0) {
      return EMPTY_ARRAY;
    }

    const sorted = [...messages].sort((a, b) => {
      if (a.ordinal !== undefined && b.ordinal !== undefined) {
        return Number(a.ordinal) - Number(b.ordinal);
      }
      return (
        new Date(a.createdAt || "").getTime() - new Date(b.createdAt || "").getTime()
      );
    });

    const filtered = sorted.filter((message) => message.role !== MessageRole.TOOL);

    return filtered;
  }, [messages]);

  // Handle send message
  const handleSendMessage = useCallback(
    async (
      content: string,
      attachmentIds?: string[],
      workflow?: string | null,
      workflowParams?: Record<string, unknown>,
      targetThread?: string | null,
      selectedPresets?: Record<string, string | null>,
    ) => {
      logger.info("🚀 Sending message:", {
        content,
        attachmentIds,
        connectionStatus,
        currentChat: currentChat?.id,
        currentWorktreeId,
        workflow,
        workflowParams,
        targetThread,
        selectedPresets,
      });

      try {
        if (!currentChat) {
          logger.info(
            "📝 Creating new chat with workspace:",
            currentWorktreeId,
          );
          // Filter out null values from selectedPresets for backend
          const presetsForBackend = selectedPresets
            ? (Object.fromEntries(
                Object.entries(selectedPresets).filter(([_, v]) => v != null),
              ) as Record<string, string>)
            : undefined;
          const newChat = await useChatStore
            .getState()
            .createChat(
              currentWorktreeId || undefined,
              content,
              attachmentIds,
              workflowParams,
              workflow,
              presetsForBackend,
            );

          useChatStore.getState().selectChat(newChat);
        } else {
          if (chatId) {
            // Send message - backend handles resume automatically if workflow is paused
            // Filter out null values from selectedPresets for backend
            const presetsForSend = selectedPresets
              ? (Object.fromEntries(
                  Object.entries(selectedPresets).filter(([_, v]) => v != null),
                ) as Record<string, string>)
              : undefined;
            await useChatStore
              .getState()
              .sendMessage(chatId, content, attachmentIds, {
                workflow: workflow || null,
                workflowParams,
                targetThread,
                selectedPresets: presetsForSend,
              });
          } else {
            throw new Error("No chat ID available");
          }
        }
      } catch (error) {
        logger.error("Error sending message:", error);
        // Show error toast to user
        toast.error(error);
      }
    },
    [currentChat, currentWorktreeId, chatId, connectionStatus],
  );

  // Handle stop streaming (pause the workflow so it can be resumed)
  const handleStopStreaming = useCallback(async () => {
    logger.info("🛑 Stop button clicked, attempting to pause...");
    const currentChatId = chatId || currentChat?.id;

    if (currentChatId) {
      logger.info("🛑 Calling pauseChat API...");
      try {
        // Stop streaming immediately for UI responsiveness
        useChatStore.getState().stopStreaming(currentChatId);
        await useChatStore.getState().pauseChat(currentChatId);
        logger.info("✅ pauseChat API call completed");
      } catch (error) {
        logger.error("pauseChat API call failed:", error);
        Sentry.captureException(error, {
          tags: { component: 'chat', operation: 'stop_streaming' },
          level: 'warning',
        });
      }
    }
  }, [chatId, currentChat?.id]);

  // Get UI state from navigation store
  const showRecentChanges = useChatNavigationStore((state) =>
    chatId ? state.showRecentChanges[chatId] || false : false,
  );

  // Toggle handlers
  const handleToggleRecentChanges = useCallback(() => {
    if (chatId) {
      useChatNavigationStore.getState().toggleRecentChanges(chatId);
    }
  }, [chatId]);

  // Fetch workflow executions for sidebar
  const { data: workflowExecutionsData } = useWorkflowExecutions(chatId);
  const workflowExecution = useMemo(() => {
    if (!workflowExecutionsData) return undefined;
    return transformWorkflowExecution(workflowExecutionsData);
  }, [workflowExecutionsData]);

  // Handle restart conversation (when workflow was lost)
  const handleRestartConversation = useCallback(async () => {
    if (!chatId) return;
    logger.info("🔄 Restart conversation requested", { chatId });
    try {
      // Send a continuation message to start a new workflow
      await useChatStore
        .getState()
        .sendMessage(chatId, "Continue", undefined, {});
      logger.info("✅ Restart conversation message sent");
    } catch (error) {
      logger.error("Failed to restart conversation:", error);
      Sentry.captureException(error, {
        tags: { component: 'chat', operation: 'restart_conversation' },
        level: 'warning',
      });
    }
  }, [chatId]);

  return (
    <ChatPresenter
      messages={processedMessages}
      approvals={approvals}
      errorEvents={errorEvents}
      infoEvents={infoEvents}
      runOutputs={runOutputs}
      chatId={chatId}
      isChatBusy={isChatBusy}
      pendingApprovals={pendingApprovals}
      hasPendingYield={hasPendingYield}
      connectionStatus={connectionStatus}
      currentActivity={currentActivity ?? undefined}
      worktreeId={currentChat?.worktreeId ?? undefined}
      projectId={currentProjectId ?? undefined}
      needsRecovery={currentChat?.needsRecovery}
      onRestartConversation={handleRestartConversation}
      onSendMessage={handleSendMessage}
      onStopStreaming={handleStopStreaming}
      isFocused={isFocused}
      isRecentChangesOpen={showRecentChanges}
      onToggleRecentChanges={handleToggleRecentChanges}
      workflowExecution={workflowExecution}
    />
  );
}