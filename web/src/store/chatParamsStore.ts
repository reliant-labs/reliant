import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { logger } from '../lib/logger';

/**
 * Persisted store for per-chat workflow params.
 * 
 * Workflows define their inputs in YAML. Users can customize these per-chat.
 * This store persists those customizations to localStorage so they survive
 * page reloads and session switches.
 */

export type WorkflowParams = Record<string, unknown>;

interface ChatParamsStore {
  // Map of chatId -> workflow params
  chatParams: Record<string, WorkflowParams>;
  
  // Params for new chats (before they have an ID)
  tempNewChatParams: WorkflowParams;
  
  // Actions
  setChatParams: (chatId: string, params: WorkflowParams) => void;
  updateChatParams: (chatId: string, params: WorkflowParams) => void;
  getChatParams: (chatId: string) => WorkflowParams;
  getChatParam: <T = unknown>(chatId: string, key: string) => T | undefined;
  
  setTempNewChatParams: (params: WorkflowParams) => void;
  updateTempNewChatParams: (params: WorkflowParams) => void;
  getTempNewChatParam: <T = unknown>(key: string) => T | undefined;
  
  removeChatParams: (chatId: string) => void;
  transferTempToChat: (chatId: string) => void;
  clearTempNewChatParams: () => void;
}

export const useChatParamsStore = create<ChatParamsStore>()(
  persist(
    (set, get) => ({
      chatParams: {},
      tempNewChatParams: {},
      
      setChatParams: (chatId, params) => {
        logger.debug('[ChatParamsStore] Setting params', { chatId: chatId.slice(0, 8), params });
        set((state) => ({
          chatParams: { ...state.chatParams, [chatId]: params },
        }));
      },
      
      updateChatParams: (chatId, params) => {
        logger.debug('[ChatParamsStore] Updating params', { chatId: chatId.slice(0, 8), params });
        set((state) => ({
          chatParams: {
            ...state.chatParams,
            [chatId]: { ...(state.chatParams[chatId] || {}), ...params },
          },
        }));
      },
      
      getChatParams: (chatId) => get().chatParams[chatId] || {},
      
      getChatParam: <T = unknown>(chatId: string, key: string) => 
        get().chatParams[chatId]?.[key] as T | undefined,
      
      setTempNewChatParams: (params) => set({ tempNewChatParams: params }),
      
      updateTempNewChatParams: (params) => {
        set((state) => ({
          tempNewChatParams: { ...state.tempNewChatParams, ...params },
        }));
      },
      
      getTempNewChatParam: <T = unknown>(key: string) => 
        get().tempNewChatParams[key] as T | undefined,
      
      removeChatParams: (chatId) => {
        set((state) => {
          const newParams = { ...state.chatParams };
          delete newParams[chatId];
          return { chatParams: newParams };
        });
      },
      
      transferTempToChat: (chatId) => {
        const temp = get().tempNewChatParams;
        if (Object.keys(temp).length > 0) {
          logger.debug('[ChatParamsStore] Transfer temp to chat', { chatId: chatId.slice(0, 8), temp });
          set((state) => ({
            chatParams: { ...state.chatParams, [chatId]: { ...temp } },
            tempNewChatParams: {}, // Clear temp params after transfer
          }));
        }
      },
      
      clearTempNewChatParams: () => {
        logger.debug('[ChatParamsStore] Clearing temp new chat params');
        set({ tempNewChatParams: {} });
      },
    }),
    {
      name: 'reliant-chat-params',
      version: 5,
      partialize: (state) => ({ 
        chatParams: state.chatParams,
        // NOTE: tempNewChatParams is intentionally NOT persisted.
        // It's transient state for composing a message before the chat is created.
        // Persisting it would cause new chats to inherit old values instead of
        // respecting workflow parameter defaults.
      }),
      migrate: (persistedState: unknown, version: number) => {
        // v1: { chatModes: { [chatId]: { autoApprove, planningMode } } }
        if (version === 1) {
          const old = persistedState as { chatModes?: Record<string, { autoApprove: boolean; planningMode: boolean }> };
          const chatParams: Record<string, WorkflowParams> = {};
          for (const [id, m] of Object.entries(old.chatModes || {})) {
            chatParams[id] = { mode: m.planningMode ? "plan" : m.autoApprove ? "auto" : "manual" };
          }
          return { chatParams };
        }
        // v2: { chatModes: { [chatId]: mode } }
        if (version === 2) {
          const old = persistedState as { chatModes?: Record<string, string>; tempNewChatMode?: string };
          const chatParams: Record<string, WorkflowParams> = {};
          for (const [id, mode] of Object.entries(old.chatModes || {})) {
            chatParams[id] = { mode };
          }
          return { chatParams };
        }
        // v3: Drop tempNewChatParams from persistence - new chats should use workflow defaults
        if (version === 3) {
          const old = persistedState as { chatParams?: Record<string, WorkflowParams> };
          return { chatParams: old.chatParams || {} };
        }
        // v4 → v5: Clear all chat params to purge ungrouped preset keys
        // (e.g. "model" instead of nested agent.model) that were stored before
        // ApplyToInputs was fixed to produce nested structure.
        if (version === 4) {
          return { chatParams: {} };
        }
        return persistedState;
      },
    }
  )
);

// Selectors
export function useChatParam<T = unknown>(chatId: string | undefined, key: string, defaultValue: T): T {
  return useChatParamsStore((state) => {
    if (chatId) {
      return (state.chatParams[chatId]?.[key] as T) ?? defaultValue;
    }
    return (state.tempNewChatParams[key] as T) ?? defaultValue;
  });
}
