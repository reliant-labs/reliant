// Debug utilities for development
import {
  StreamingState,
} from "../gen/reliant/v1/chat_pb";
import { logger } from './logger';
import { isDev } from './constants';
import { useChatStore } from '../store/chatStore';
import { useThreadActivityStore } from '../store/threadActivityStore';
import { useActivityStore, ChatActivity } from '../store/activityStore';
import { queryClient } from './query-client';
import { approvalKeys } from '../hooks/approval-queries';
import { getMessagesFromCache } from '../hooks/message-queries';
import { ApprovalStatus, type ToolApprovalRequest } from '../api/approval-grpc';

class DebugLogger {
  private logs: string[] = [];
  private maxLogs = 1000;

  constructor() {
    if (isDev) {
      // Override console methods in development
      const originalConsole = { ...console };

      (["log", "info", "warn", "error", "debug"] as const).forEach((method) => {
        const originalMethod = originalConsole[method];
        console[method] = (...args: unknown[]) => {
          const timestamp = new Date().toISOString();
          const logEntry = `[${timestamp}] ${method.toUpperCase()}: ${args
            .map((arg) => {
              if (arg == null) return String(arg);
              if (typeof arg !== "object") return String(arg);
              // Skip DOM nodes and other non-plain objects that cause circular refs
              if (arg instanceof Element || arg instanceof Event) return String(arg);
              try {
                return JSON.stringify(arg, null, 2);
              } catch {
                return String(arg);
              }
            })
            .join(" ")}`;

          this.logs.push(logEntry);
          if (this.logs.length > this.maxLogs) {
            this.logs.shift();
          }

          // Also call original console method
          originalMethod(...args);

          // Write to file in development
          this.writeToFile(logEntry);
        };
      });

      logger.info("Debug logging initialized");
    }
  }

  private writeToFile(logEntry: string) {
    // For browser environment, we'll use localStorage as a fallback
    try {
      const existingLogs = localStorage.getItem("debug-logs") || "";
      const updatedLogs = existingLogs + logEntry + "\n";

      // Keep only last 10KB of logs
      if (updatedLogs.length > 10000) {
        const truncated = updatedLogs.slice(-10000);
        localStorage.setItem("debug-logs", truncated);
      } else {
        localStorage.setItem("debug-logs", updatedLogs);
      }
    } catch {
      // Silently fail if localStorage is full
    }
  }

  public downloadLogs() {
    if (!isDev) return;

    const logs = localStorage.getItem("debug-logs") || "";
    const blob = new Blob([logs], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `debug-logs-${Date.now()}.txt`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }

  public clearLogs() {
    if (!isDev) return;

    this.logs = [];
    localStorage.removeItem("debug-logs");
    logger.info("Debug logs cleared");
  }

  public getLogs(): string[] {
    return [...this.logs];
  }
}

export const debugLogger = new DebugLogger();

// Add global functions for easy access in dev console
interface DebugWindow extends Window {
  downloadLogs: () => void;
  clearLogs: () => void;
  getLogs: () => unknown[];
  // Chat recovery functions
  resetStuckChat: (chatId: string) => void;
  inspectChatState: (chatId: string) => void;
}

if (isDev && typeof window !== "undefined") {
  const debugWindow = window as unknown as DebugWindow;
  debugWindow.downloadLogs = () => debugLogger.downloadLogs();
  debugWindow.clearLogs = () => debugLogger.clearLogs();
  debugWindow.getLogs = () => debugLogger.getLogs();
  
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
      isActive: (activityState.activities.get(chatId) ?? ChatActivity.IDLE) >= ChatActivity.RUNNING,
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