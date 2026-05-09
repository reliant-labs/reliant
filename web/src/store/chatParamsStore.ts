import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { logger } from '../lib/logger';

/**
 * Persisted store for per-chat workflow params and preset selections.
 *
 * `chatParams` holds workflow inputs that the user customized for a given
 * chat — these are sent to the backend verbatim on each send.
 *
 * `chatPresets` holds preset selections (group → preset name) for that chat;
 * the chat record is the eventual source of truth, but we cache them here so
 * the UI state survives remounts before the chat record syncs back.
 *
 * The `tempNewChat*` siblings hold the same shape for a new chat that
 * doesn't have an ID yet. They are intentionally NOT persisted — composing
 * a new chat is a transient action that should respect workflow defaults
 * after a reload.
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
  persist(
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
    }),
    {
      name: 'reliant-chat-params',
      version: 6,
      partialize: (state) => ({
        chatParams: state.chatParams,
        chatPresets: state.chatPresets,
        // tempNewChat* fields are intentionally NOT persisted — composing a
        // new chat is transient and should respect workflow defaults on reload.
      }),
      migrate: (persistedState: unknown, version: number) => {
        // v1: { chatModes: { [chatId]: { autoApprove, planningMode } } }
        if (version === 1) {
          const old = persistedState as { chatModes?: Record<string, { autoApprove: boolean; planningMode: boolean }> };
          const chatParams: Record<string, WorkflowParams> = {};
          for (const [id, m] of Object.entries(old.chatModes || {})) {
            chatParams[id] = { mode: m.planningMode ? "plan" : m.autoApprove ? "auto" : "manual" };
          }
          return { chatParams, chatPresets: {} };
        }
        // v2: { chatModes: { [chatId]: mode } }
        if (version === 2) {
          const old = persistedState as { chatModes?: Record<string, string>; tempNewChatMode?: string };
          const chatParams: Record<string, WorkflowParams> = {};
          for (const [id, mode] of Object.entries(old.chatModes || {})) {
            chatParams[id] = { mode };
          }
          return { chatParams, chatPresets: {} };
        }
        // v3: Drop tempNewChatParams from persistence.
        if (version === 3) {
          const old = persistedState as { chatParams?: Record<string, WorkflowParams> };
          return { chatParams: old.chatParams || {}, chatPresets: {} };
        }
        // v4 → v5: Clear all chat params to purge ungrouped preset keys.
        if (version === 4) {
          return { chatParams: {}, chatPresets: {} };
        }
        // v5 → v6: Lift `__selectedPresets` out of chatParams[*] into a sibling
        // `chatPresets` map. Strip any other `__`-prefixed metadata keys that
        // might have leaked into chatParams (e.g. `__selectedWorkflow`).
        if (version === 5) {
          const old = persistedState as { chatParams?: Record<string, WorkflowParams> };
          const cleanParams: Record<string, WorkflowParams> = {};
          const chatPresets: Record<string, PresetSelections> = {};
          for (const [id, params] of Object.entries(old.chatParams ?? {})) {
            const filtered: WorkflowParams = {};
            for (const [k, v] of Object.entries(params)) {
              if (!k.startsWith("__")) filtered[k] = v;
            }
            cleanParams[id] = filtered;
            const presets = (params as { __selectedPresets?: unknown }).__selectedPresets;
            if (presets && typeof presets === "object" && !Array.isArray(presets)) {
              chatPresets[id] = presets as PresetSelections;
            }
          }
          return { chatParams: cleanParams, chatPresets };
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
