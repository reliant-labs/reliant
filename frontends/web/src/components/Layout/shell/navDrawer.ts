// Copyright (c) 2026 Reliant Labs

/**
 * The narrow window's nav drawer (Layout/shell/AppShell.tsx): open or not.
 * Local to the frame, never persisted. shellFrame.tsx re-exports it; it is a
 * module of its own so a reader that only watches the drawer (the public
 * demo's corner cards, Forge/Demo/DemoCorner.tsx) does not load the frame's
 * controls, the keyboard map and the workspace store with it.
 */

import { create } from "zustand";

export const useNavDrawer = create<{ open: boolean; setOpen: (open: boolean) => void }>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
}));
