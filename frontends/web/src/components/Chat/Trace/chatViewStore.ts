// Copyright (c) 2026 Reliant Labs

/**
 * Which chat has its Trace view open. One chat at most: opening another chat
 * shows its conversation, and coming back to this one finds its Trace still
 * open until a different chat's Trace replaces it.
 *
 * A store rather than ChatPresenter's own state: the presenter can remount
 * while a chat loads, and with local state the browser pass saw the tab fall
 * back to Chat a moment after Trace was chosen. In memory only: a reload
 * opens the conversation.
 */

import { create } from "zustand";

import type { ChatView } from "./ChatViewTabs";

interface ChatViewState {
  traceChatId: string | null;
  setView: (chatId: string | null, view: ChatView) => void;
}

export const useChatViewStore = create<ChatViewState>((set) => ({
  traceChatId: null,
  setView: (chatId, view) =>
    set((state) => {
      if (view === "trace") return { traceChatId: chatId };
      return state.traceChatId === chatId ? { traceChatId: null } : state;
    }),
}));

/** The view chatId opens on. */
export function useChatView(chatId: string | null): ChatView {
  return useChatViewStore((state) => (chatId && state.traceChatId === chatId ? "trace" : "chat"));
}
