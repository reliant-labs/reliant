// Debug utilities for development
import {
  StreamingState,
} from "../gen/reliant/v1/chat_pb";
import { logger } from './logger';
import { isDev } from './constants';
import { useChatStore } from '../store/chatStore';
import { useThreadActivityStore } from '../store/threadActivityStore';
import { useActivityStore, ChatActivity, isBusyActivity } from '../store/activityStore';
import { queryClient } from './query-client';
import { approvalKeys } from '../hooks/approval-queries';
import { getMessagesFromCache } from '../hooks/message-queries';
import { ApprovalStatus, type ToolApprovalRequest } from '../api/approval-grpc';

// This file used to carry a third console override — a DebugLogger that
// pretty-printed every argument with JSON.stringify(arg, null, 2) and then did a
// synchronous localStorage read + write of up to 10 KB, per line, on the main
// thread. It is gone, and so are the window.downloadLogs/clearLogs/getLogs
// helpers it backed: the dev log is already on disk at
// control-plane/.forge/logs/dev/frontend_reliant-web.log, complete and
// greppable, which is strictly better than a 10 KB localStorage ring buffer you
// have to download out of the browser to read.
//
// What remains are the chat-recovery helpers below, which are the reason
// main.tsx still imports this module for its side effects.

// Add global functions for easy access in dev console
interface DebugWindow extends Window {
  // Chat recovery functions
  resetStuckChat: (chatId: string) => void;
  inspectChatState: (chatId: string) => void;
}

if (isDev && typeof window !== "undefined") {
  const debugWindow = window as unknown as DebugWindow;

  // Chat recovery functions
  debugWindow.resetStuckChat = (chatId: string) => {
    logger.warn('🔧 [DEBUG] Resetting stuck chat:', chatId);
    const store = useChatStore.getState();
    if (typeof store.forceResetChatToIdle === 'function') {
      store.forceResetChatToIdle(chatId);
      logger.info('✅ Chat reset complete');
    } else {
      logger.error('❌ forceResetChatToIdle function not found in store');
    }
  };
  
  debugWindow.inspectChatState = (chatId: string) => {
    const state = useChatStore.getState();
    const activityState = useActivityStore.getState();
    const chatState = {
      chatId: chatId,
      isActive: isBusyActivity(activityState.activities.get(chatId)),
      activity: activityState.activities.get(chatId) ?? ChatActivity.IDLE,
      pendingApprovals:
        (queryClient
          .getQueryData<ToolApprovalRequest[]>(approvalKeys.list(chatId))
          ?.filter((a) => a.status === ApprovalStatus.PENDING).length) || 0,
      activeThreads: useThreadActivityStore.getState().threads[chatId]?.length || 0,
      toolCallStates: state.toolCallStates[chatId]?.size || 0,
      streamingMessages: getMessagesFromCache(chatId).filter(
        (m) => m.streamingState === StreamingState.STREAMING,
      ).length,
      messageCount: getMessagesFromCache(chatId).length,
    };
    console.table(chatState);
    console.log('Full state:', chatState);
    return chatState;
  };
  
  logger.info('🔧 Debug functions available: resetStuckChat(chatId), inspectChatState(chatId)');
}