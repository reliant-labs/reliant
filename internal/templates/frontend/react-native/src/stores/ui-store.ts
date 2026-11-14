import { create } from "zustand";

/**
 * Base UI store for React Native — shared client-side state.
 *
 * Scope: ONLY app-wide, cross-component UI state that doesn't fit anywhere
 * else (drawer open, bottom sheet open). For everything else, prefer:
 *
 *   - Small modals / action sheets bound to one action → `useState` in the
 *     component that owns the action. Lift to a store ONLY if two unrelated
 *     components both need to read the same modal's open state.
 *
 *   - Navigation-replacing surfaces (settings screen, detail view, full
 *     drawer) → SCREENS via Expo Router. If the hardware/back button should
 *     dismiss it, it's a route, not a Zustand field. See the `frontend/state`
 *     skill.
 *
 *   - Server data (the current user, fetched lists) → generated React Query
 *     hooks. Never copy backend data into Zustand.
 *
 * Extend by creating domain stores in src/stores/:
 *   src/stores/editor-store.ts
 *
 * Always subscribe to slices, not the whole store:
 *   const open = useUiStore(s => s.drawerOpen);  // GOOD
 *   const store = useUiStore();  // BAD
 */

interface UiState {
  // Navigation drawer
  drawerOpen: boolean;
  toggleDrawer: () => void;
  setDrawerOpen: (open: boolean) => void;

  // Bottom sheet
  bottomSheetOpen: boolean;
  bottomSheetContent: string | null;
  openBottomSheet: (content: string) => void;
  closeBottomSheet: () => void;
}

export const useUiStore = create<UiState>((set) => ({
  // Navigation drawer
  drawerOpen: false,
  toggleDrawer: () => set((s) => ({ drawerOpen: !s.drawerOpen })),
  setDrawerOpen: (open) => set({ drawerOpen: open }),

  // Bottom sheet
  bottomSheetOpen: false,
  bottomSheetContent: null,
  openBottomSheet: (content) => set({ bottomSheetOpen: true, bottomSheetContent: content }),
  closeBottomSheet: () => set({ bottomSheetOpen: false, bottomSheetContent: null }),
}));
