/**
 * Open/close state for the mobile navigation drawer.
 *
 * A store rather than local `MobileShell` state because the trigger button
 * has to live inside each screen's own header (for visual consistency with
 * that screen's existing chrome) while the drawer itself is mounted once at
 * the shell level (so it portals above every screen regardless of which one
 * is routed). Passing a callback down through props would mean plumbing it
 * through every screen; a store lets any header opt in with one line.
 */

import { create } from "zustand";

interface MobileDrawerStore {
  isOpen: boolean;
  open: () => void;
  close: () => void;
}

export const useMobileDrawerStore = create<MobileDrawerStore>((set) => ({
  isOpen: false,
  open: () => set({ isOpen: true }),
  close: () => set({ isOpen: false }),
}));
