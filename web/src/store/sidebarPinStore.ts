// Copyright (c) 2025 Reliant Labs

import { create } from "zustand";

/**
 * The one exception to "the open chat stays in the sidebar": a chat the user
 * just moved out of the list themselves ("Move back to Runs"). Pinning it
 * anyway would make the action look like it did nothing.
 *
 * The release lasts while that chat stays open. Opening another chat ends it,
 * so reopening the run later pins it again like any other open chat.
 *
 * A store rather than component state because the sidebar mounts twice (the
 * docked panel and the hover overlay), and both must agree.
 */
interface SidebarPinState {
  releasedChatId: string | null;
  release: (chatId: string) => void;
  /** Drop the release once a different chat is open. */
  forgetUnless: (activeChatId: string | null) => void;
}

export const useSidebarPinStore = create<SidebarPinState>((set, get) => ({
  releasedChatId: null,
  release: (chatId) => set({ releasedChatId: chatId }),
  forgetUnless: (activeChatId) => {
    const { releasedChatId } = get();
    if (releasedChatId !== null && releasedChatId !== activeChatId) set({ releasedChatId: null });
  },
}));
