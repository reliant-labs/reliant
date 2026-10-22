import { create } from "zustand";
import { persist } from "zustand/middleware";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import { logger } from "../lib/logger";

const getInitialShowHiddenFiles = (): boolean => {
  const saved = settingsSync.getSetting(SETTINGS_KEYS.SHOW_HIDDEN_FILES, "true");
  return saved === "true";
};

interface UIState {
  // File browser settings
  showHiddenFiles: boolean;
  setShowHiddenFiles: (show: boolean) => void;

  // Browser view state (for Modern UI)
  isBrowserActive: boolean;
  setIsBrowserActive: (active: boolean) => void;
  toggleBrowserView: () => void;

  // Background navigation overlay state
  isNavigationOverlayOpen: boolean;
  openNavigationOverlay: () => void;
  closeNavigationOverlay: () => void;
  toggleNavigationOverlay: () => void;
  // Track tabs opened from navigation overlay
  navigationOverlayOpenedTabs: Set<string>;
  linkTabToNavigationOverlay: (tabId: string) => void;
  unlinkTabFromNavigationOverlay: (tabId: string) => void;
}

export const useUIStore = create<UIState>()(
  persist(
    (set, get) => ({
      showHiddenFiles: getInitialShowHiddenFiles(),
      setShowHiddenFiles: (show) => {
        set({ showHiddenFiles: show });
        // Sync to database
        settingsSync.setSetting(SETTINGS_KEYS.SHOW_HIDDEN_FILES, show.toString()).catch((e) => logger.error('Failed to sync hidden files setting:', e));
      },

      // Browser view state
      isBrowserActive: false,
      setIsBrowserActive: (active) => {
        set({ isBrowserActive: active });
      },

      toggleBrowserView: () => {
        const current = get().isBrowserActive;
        set({ isBrowserActive: !current });
      },

      // Navigation overlay state (not persisted)
      isNavigationOverlayOpen: false,
      navigationOverlayOpenedTabs: new Set<string>(),

      openNavigationOverlay: () => {
        set({ isNavigationOverlayOpen: true });
      },

      closeNavigationOverlay: () => {
        set({
          isNavigationOverlayOpen: false,
          navigationOverlayOpenedTabs: new Set<string>(), // Clear tracked tabs
        });
      },

      toggleNavigationOverlay: () => {
        const isOpen = get().isNavigationOverlayOpen;
        if (isOpen) {
          get().closeNavigationOverlay();
        } else {
          get().openNavigationOverlay();
        }
      },

      linkTabToNavigationOverlay: (tabId: string) => {
        set((state) => {
          const newSet = new Set(state.navigationOverlayOpenedTabs);
          newSet.add(tabId);
          return { navigationOverlayOpenedTabs: newSet };
        });
      },

      unlinkTabFromNavigationOverlay: (tabId: string) => {
        set((state) => {
          const newSet = new Set(state.navigationOverlayOpenedTabs);
          newSet.delete(tabId);

          // If no more tabs are linked, close the overlay
          const shouldCloseOverlay =
            newSet.size === 0 && state.isNavigationOverlayOpen;

          return {
            navigationOverlayOpenedTabs: newSet,
            isNavigationOverlayOpen: shouldCloseOverlay
              ? false
              : state.isNavigationOverlayOpen,
          };
        });
      },
    }),
    {
      name: "ui-storage",
      // Don't persist navigation overlay state
      partialize: (state) => ({
        showHiddenFiles: state.showHiddenFiles,
      }),
    }
  )
);
