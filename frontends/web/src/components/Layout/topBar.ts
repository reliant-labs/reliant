// Copyright (c) 2026 Reliant Labs

/**
 * One top bar for the chat screen (owner, 2026-11-23): the open chat's header
 * (title, threads, runs, context ring, ⋯) renders INSIDE the app bar, after
 * its project / branch path, instead of in a second row under it.
 *
 * The bar owns an empty slot element; ChatHeader portals into it. One chat
 * holds the slot at a time (`owner`): any other ChatHeader on screen (the
 * run detail's, a side-by-side workflow viewer's) keeps its own row.
 */

import { create } from "zustand";

interface TopBarSlotState {
  el: HTMLElement | null;
  owner: symbol | null;
  setEl: (el: HTMLElement | null) => void;
  claim: (owner: symbol) => boolean;
  release: (owner: symbol) => void;
}

export const useTopBarSlot = create<TopBarSlotState>((set, get) => ({
  el: null,
  owner: null,
  setEl: (el) => set({ el }),
  claim: (owner) => {
    const current = get().owner;
    if (current && current !== owner) return false;
    if (current !== owner) set({ owner });
    return true;
  },
  release: (owner) => {
    if (get().owner === owner) set({ owner: null });
  },
}));
