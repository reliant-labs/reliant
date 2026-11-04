/**
 * Text another screen wants waiting in the new-chat composer when the user
 * arrives — "Ask Reliant" on an issue, a trace, an alert, a panel.
 *
 * A one-shot mailbox, not a draft: the new-chat draft (workspaceStateStore) is
 * the user's own words and survives reloads, and writing a prompt into it would
 * silently replace what they had typed. The composer already applies a
 * `ComposerPrefill` once per id (ChatInput), so this only carries one to it
 * across a navigation, and whoever takes it owns it. Nothing is sent: the user
 * reads the text, edits it and presses send.
 *
 * `onChatCreated` is called with the chat's id when the user does send — the
 * first moment the chat exists. It outlives the screen that offered it (the
 * console unmounts on the way), so it must not close over component state.
 */

import { create } from "zustand";

export interface ComposerHandoff {
  /** Counts up from 1, so the composer applies each handoff once. */
  id: number;
  projectId: string;
  worktreeId: string;
  text: string;
  onChatCreated?: (chatId: string) => void;
  offeredAt: number;
}

/** An offer nobody took this long ago is stale: the navigation that carried it never landed. */
export const HANDOFF_TTL_MS = 2 * 60_000;

interface ComposerHandoffStore {
  pending: ComposerHandoff | null;
  offer: (handoff: Omit<ComposerHandoff, "id" | "offeredAt">) => void;
  /** The pending handoff for this project, removed from the store; null when none or stale. */
  take: (projectId: string, now?: number) => ComposerHandoff | null;
}

let lastId = 0;

export const useComposerHandoffStore = create<ComposerHandoffStore>((set, get) => ({
  pending: null,
  offer: (handoff) => set({ pending: { ...handoff, id: ++lastId, offeredAt: Date.now() } }),
  take: (projectId, now = Date.now()) => {
    const { pending } = get();
    if (!pending) return null;
    if (now - pending.offeredAt > HANDOFF_TTL_MS) {
      set({ pending: null });
      return null;
    }
    if (pending.projectId !== projectId) return null;
    set({ pending: null });
    return pending;
  },
}));
