// Copyright (c) 2025 Reliant Labs

/**
 * The machine the chat UI is pointed at right now: what the new-chat
 * machine picker resolves to (NewChatView), or the open chat's machine.
 *
 * Adding a project or a folder starts from this machine rather than blindly
 * from the user's default one: a user looking at a chat on their laptop who
 * clicks Add project means a folder on their laptop.
 */

import { create } from "zustand";

interface CurrentMachineState {
  /** A daemon id, or null when the UI is on no machine (or none is known). */
  daemonId: string | null;
  setCurrentMachine: (daemonId: string | null) => void;
}

export const useCurrentMachineStore = create<CurrentMachineState>((set) => ({
  daemonId: null,
  setCurrentMachine: (daemonId) => set((state) => (state.daemonId === daemonId ? state : { daemonId })),
}));
