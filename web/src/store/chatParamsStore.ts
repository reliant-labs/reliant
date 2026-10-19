import { create } from 'zustand';
import { logger } from '../lib/logger';

/**
 * In-memory draft of per-chat workflow params and preset selections.
 *
 * A chat's params live on the server (its ChatSelection), and the composer
 * seeds from there; nothing here survives a reload, and nothing here is read as
 * the chat's truth. This is the composer's working copy: `chatParams` holds the
 * inputs as the user is editing them, `chatPresets` the preset selections
 * (group → preset name), and both are what a send or a sync pushes.
 *
 * What an older build persisted in localStorage (`reliant-chat-params`) is read
 * once per chat by lib/legacyChatParams to seed a chat the server has no
 * selection for, and then deleted.
 *
 * The `tempNewChat*` siblings hold the same shape for a new chat that doesn't
 * have an ID yet, until it is created and they transfer to its id.
 */

export type WorkflowParams = Record<string, unknown>;
export type PresetSelections = Record<string, string | null>;

interface ChatParamsStore {
  // Per-chat state
  chatParams: Record<string, WorkflowParams>;
  chatPresets: Record<string, PresetSelections>;

  // Temp state for the next new chat (cleared on send / clearTempNewChat)
  tempNewChatParams: WorkflowParams;
  tempNewChatWorkflow: string | null;
  tempNewChatPresets: PresetSelections;

  // Per-chat actions
  setChatParams: (chatId: string, params: WorkflowParams) => void;
  updateChatParams: (chatId: string, params: WorkflowParams) => void;
  getChatParams: (chatId: string) => WorkflowParams;
  getChatParam: <T = unknown>(chatId: string, key: string) => T | undefined;
  setChatPresets: (chatId: string, presets: PresetSelections) => void;
  getChatPresets: (chatId: string) => PresetSelections;
  removeChatParams: (chatId: string) => void;

  // Temp actions
  setTempNewChatParams: (params: WorkflowParams) => void;
  updateTempNewChatParams: (params: WorkflowParams) => void;
  getTempNewChatParam: <T = unknown>(key: string) => T | undefined;
  setTempNewChatWorkflow: (workflow: string | null) => void;
  setTempNewChatPresets: (presets: PresetSelections) => void;
  transferTempToChat: (chatId: string) => void;
  clearTempNewChatParams: () => void;
}

export const useChatParamsStore = create<ChatParamsStore>()(
    (set, get) => ({
      chatParams: {},
      chatPresets: {},
      tempNewChatParams: {},
      tempNewChatWorkflow: null,
      tempNewChatPresets: {},

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

      setChatPresets: (chatId, presets) => {
        set((state) => ({
          chatPresets: { ...state.chatPresets, [chatId]: presets },
        }));
      },

      getChatPresets: (chatId) => get().chatPresets[chatId] || {},

      setTempNewChatParams: (params) => set({ tempNewChatParams: params }),

      updateTempNewChatParams: (params) => {
        set((state) => ({
          tempNewChatParams: { ...state.tempNewChatParams, ...params },
        }));
      },

      getTempNewChatParam: <T = unknown>(key: string) =>
        get().tempNewChatParams[key] as T | undefined,

      setTempNewChatWorkflow: (workflow) => set({ tempNewChatWorkflow: workflow }),

      setTempNewChatPresets: (presets) => set({ tempNewChatPresets: presets }),

      removeChatParams: (chatId) => {
        set((state) => {
          const newParams = { ...state.chatParams };
          const newPresets = { ...state.chatPresets };
          delete newParams[chatId];
          delete newPresets[chatId];
          return { chatParams: newParams, chatPresets: newPresets };
        });
      },

      transferTempToChat: (chatId) => {
        const { tempNewChatParams, tempNewChatPresets } = get();
        const hasParams = Object.keys(tempNewChatParams).length > 0;
        const hasPresets = Object.keys(tempNewChatPresets).length > 0;
        if (!hasParams && !hasPresets) return;

        logger.debug('[ChatParamsStore] Transfer temp to chat', {
          chatId: chatId.slice(0, 8),
          tempNewChatParams,
          tempNewChatPresets,
        });
        set((state) => ({
          chatParams: hasParams
            ? { ...state.chatParams, [chatId]: { ...tempNewChatParams } }
            : state.chatParams,
          chatPresets: hasPresets
            ? { ...state.chatPresets, [chatId]: { ...tempNewChatPresets } }
            : state.chatPresets,
          tempNewChatParams: {},
          tempNewChatWorkflow: null,
          tempNewChatPresets: {},
        }));
      },

      clearTempNewChatParams: () => {
        logger.debug('[ChatParamsStore] Clearing temp new chat state');
        set({
          tempNewChatParams: {},
          tempNewChatWorkflow: null,
          tempNewChatPresets: {},
        });
      },
    })
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
