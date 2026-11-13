// Copyright (c) 2025 Reliant Labs

/**
 * The machine a screen that opens the new-chat view wants that chat to run on,
 * carried across the navigation: Add project's folder or clone names the
 * machine the user just picked, and an existing project the folder attached to
 * may already be installed on another (the default) machine, which the
 * new-chat view would otherwise choose.
 *
 * A one-shot mailbox, like composerHandoffStore: NewChatView takes it once the
 * project it names is current, and applies it as the user's own machine
 * choice; nothing else reads it.
 */

import { create } from "zustand";

export interface NewChatMachineRequest {
  projectId: string;
  daemonId: string;
}

interface NewChatMachineStore {
  pending: NewChatMachineRequest | null;
  offer: (request: NewChatMachineRequest) => void;
  /** The pending request for this project, removed from the store; null when none. */
  take: (projectId: string) => string | null;
}

export const useNewChatMachineStore = create<NewChatMachineStore>((set, get) => ({
  pending: null,
  offer: (request) => set({ pending: request }),
  take: (projectId) => {
    const pending = get().pending;
    if (!pending || pending.projectId !== projectId) return null;
    set({ pending: null });
    return pending.daemonId;
  },
}));
