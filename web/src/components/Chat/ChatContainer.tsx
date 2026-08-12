/**
 * ChatContainer - State management for tab-based chats
 *
 * Subscribes to stores and manages state for a specific tab.
 * Passes all data as props to ChatPresenter for rendering.
 */

import { useMemo, useCallback, useState, useEffect } from "react";
import { MessageRole } from "../../gen/reliant/v1/chat_pb";
import { sortMessagesForDisplay } from "../../lib/messageOrder";
import { ChatPresenter } from "./ChatPresenter";
import {
  useChat,
  useChatMessages,
  useErrorEvents,
  useInfoEvents,
  useRunOutputs,
  useStreamingMessages,
  useDiscussMode,
  useHasOlderMessages,
} from "../../store/chatStoreHooks";
import {
  usePendingApprovals,
  useApprovals,
  usePendingQuestion,
} from "../../hooks/approval-queries";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { useIsChatRunning } from "../../store/activityStore";
import { useChatStore } from "../../store/chatStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { transformWorkflowExecution } from "./ExecutionSidebar";
import * as Sentry from "@sentry/react";
import { logger } from "../../lib/logger";
import { toast } from "../../lib/toast-manager";
import { isDaemonConnectingError } from "../../lib/daemon-errors";
import { sendWithDaemonWait } from "../../lib/daemon-retry";

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

  const { data: approvals = [] } = useApprovals(chatId);
  const errorEvents = useErrorEvents(chatId);
  const infoEvents = useInfoEvents(chatId);
  const runOutputs = useRunOutputs(chatId);
  const currentChat = useChat(chatId);
  const connectionStatus = useGlobalUpdatesStore((s) => s.connectionStatus);
  const isChatBusy = useIsChatRunning(chatId);
  const { data: pendingApprovals = [] } = usePendingApprovals(chatId);
  const isDiscussMode = useDiscussMode(chatId);
  const { data: pendingQuestion } = usePendingQuestion(chatId);

  // Self-healing subscription invariant: this component renders the active
  // chat, so it's the natural place to assert "the rendered chat is the
  // subscribed chat" — re-checked whenever the chat or the connection state
  // changes, not just once at selection time. Without this, another
  // component stealing the single subscription slot (e.g. WorkflowBuilderChat
  // subscribing to its own chat, or a reconnect forwarding a stale id) leaves
  // this chat silently unsubscribed with no error and no recovery path short
  // of a manual refresh. See globalUpdatesStore.reconcileChatSubscription.
  const reconcileChatSubscription = useGlobalUpdatesStore(
    (s) => s.reconcileChatSubscription,
  );
  useEffect(() => {
    reconcileChatSubscription(chatId);
  }, [chatId, connectionStatus, reconcileChatSubscription]);

  // Process messages: canonical order (lib/messageOrder), filter out tool messages
  const processedMessages = useMemo(() => {
    if (messages.length === 0) {
      return EMPTY_ARRAY;
    }

    const sorted = sortMessagesForDisplay(messages, chatId || "");

    const filtered = sorted.filter((message) => message.role !== MessageRole.TOOL);

    return filtered;
  }, [messages, chatId]);

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

      // The send is wrapped so a machine that is still coming online defers the
      // message instead of rejecting it. The composer only clears once this
      // resolves, so the user's text survives either way.
      const performSend = async () => {
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
      };

      try {
        await sendWithDaemonWait({
          action: performSend,
          onWaiting: () => {
            // Only fires if the first attempt was actually deferred, so a
            // healthy send stays silent.
            toast.info("Your machine is starting — this message will send automatically.");
          },
        });
      } catch (error) {
        logger.error("Error sending message:", error);
        if (isDaemonConnectingError(error)) {
          // Retried for the full budget and the machine never came up. This is
          // now a real failure, so say so plainly rather than implying it will
          // resolve on its own.
          toast.error(
            new Error("Your machine didn't come online, so the message wasn't sent."),
          );
        } else {
          // Show error toast to user
          toast.error(error);
        }
        throw error;
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

  const handleToggleDiscuss = useCallback(() => {
    if (!chatId) return;
    const store = useChatStore.getState();
    store.setDiscussMode(chatId, !store.discussMode[chatId]);
  }, [chatId]);

  // --- Scroll-back paging ---
  // The initial snapshot is bounded to the newest messages, so older history is
  // fetched on demand when the user scrolls to the top of the timeline.
  // hasOlderMessages comes from the server (via the message envelope); the
  // in-flight flag is local because loadOlderMessages is an imperative store
  // action rather than a query. The store guards against duplicate concurrent
  // fetches independently — this flag exists to drive the spinner.
  const hasOlderMessages = useHasOlderMessages(chatId);
  const [isLoadingOlderMessages, setIsLoadingOlderMessages] = useState(false);

  const handleLoadOlderMessages = useCallback(async () => {
    if (!chatId) return;
    setIsLoadingOlderMessages(true);
    try {
      await useChatStore.getState().loadOlderMessages(chatId);
    } finally {
      setIsLoadingOlderMessages(false);
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
      connectionStatus={connectionStatus}
      worktreeId={currentChat?.worktreeId ?? undefined}
      projectId={currentProjectId ?? undefined}
      needsRecovery={currentChat?.needsRecovery}
      onRestartConversation={handleRestartConversation}
      onSendMessage={handleSendMessage}
      onStopStreaming={handleStopStreaming}
      isFocused={isFocused}
      workflowExecution={workflowExecution}
      isDiscussMode={isDiscussMode}
      onToggleDiscuss={handleToggleDiscuss}
      hasPendingQuestion={!!pendingQuestion}
      onLoadOlderMessages={handleLoadOlderMessages}
      isLoadingOlderMessages={isLoadingOlderMessages}
      hasOlderMessages={hasOlderMessages}
    />
  );
}