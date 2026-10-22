import { create } from "zustand";
import { persist } from "zustand/middleware";
import { logger } from "../lib/logger";

export interface BrowserTab {
  id: string;
  title: string;
  url: string;
  favicon?: string;
  isLoading: boolean;
  canGoBack: boolean;
  canGoForward: boolean;
  worktreeId: string; // Required: Track which workspace this tab belongs to
  projectId?: string; // Track which project this tab belongs to
  paneId?: string; // Track which pane this tab belongs to
  createdAt: Date;
}

interface BrowserState {
  tabs: BrowserTab[];
  activeTabId: string | null;

  // Actions
  createTab: (worktreeId: string, url?: string, projectId?: string, paneId?: string) => Promise<string>;
  closeTab: (id: string) => void;
  closeWorktreeTabs: (worktreeId: string) => void; // Close all tabs for a workspace
  setActiveTab: (id: string) => void;
  getActiveTab: () => BrowserTab | null;
  getProjectTabs: (projectId?: string) => BrowserTab[];
  getWorktreeTabs: (worktreeId: string) => BrowserTab[]; // Get tabs for a workspace
  updateTabUrl: (id: string, url: string) => void;
  updateTabTitle: (id: string, title: string) => void;
  updateTabFavicon: (id: string, favicon: string) => void;
  updateTabLoading: (id: string, isLoading: boolean) => void;
  updateTabNavigation: (id: string, canGoBack: boolean, canGoForward: boolean) => void;
  updateTabPane: (id: string, paneId: string) => void;
  navigateTab: (id: string, url: string) => void;
  goBack: (id: string) => void;
  goForward: (id: string) => void;
  reload: (id: string) => void;
}

// Define what we want to persist
type PersistedBrowserState = Pick<BrowserState, 'tabs' | 'activeTabId'>;

export const useBrowserStore = create(
  persist<BrowserState, [], [], PersistedBrowserState>(
    (set, get) => ({
      tabs: [],
      activeTabId: null,

      createTab: async (worktreeId: string, url?: string, projectId?: string, paneId?: string) => {
        // If no URL provided, use the default page from settings
        let finalUrl = url;
        if (!finalUrl) {
          try {
            const { api } = await import("../api/client");
            const preferences = await api.settings.getPreferences();
            logger.debug("[BrowserStore] Loaded preferences:", preferences);
            finalUrl = ((preferences as unknown as Record<string, unknown>).browserDefaultPage as string) || "https://www.google.com";
            logger.debug("[BrowserStore] Using default page:", finalUrl);
          } catch (error) {
            logger.warn("[BrowserStore] Failed to load default page, using fallback", error);
            finalUrl = "https://www.google.com";
          }
        } else {
          logger.debug("[BrowserStore] URL provided, using:", finalUrl);
        }
        const id = `browser-tab-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
        const tab: BrowserTab = {
          id,
          title: "New Tab",
          url: finalUrl,
          isLoading: true,
          canGoBack: false,
          canGoForward: false,
          worktreeId,
          projectId,
          paneId,
          createdAt: new Date(),
        };

        set((state) => ({
          tabs: [...state.tabs, tab],
          activeTabId: id,
        }));

        logger.info("[BrowserStore] Created tab", { id, url: finalUrl, worktreeId, projectId, paneId });

        return id;
      },

      closeTab: (id: string) => {
        set((state) => {
          const tabs = state.tabs.filter((t) => t.id !== id);
          let activeTabId = state.activeTabId;

          // If closing active tab, switch to another
          if (activeTabId === id) {
            activeTabId = tabs.length > 0 ? tabs[tabs.length - 1].id : null;
          }

          return { tabs, activeTabId };
        });

        logger.info("[BrowserStore] Closed tab", { id });
      },

      closeWorktreeTabs: (worktreeId: string) => {
        set((state) => {
          const tabs = state.tabs.filter((t) => t.worktreeId !== worktreeId);
          let activeTabId = state.activeTabId;

          // If active tab was from this worktree, switch to another
          const activeTab = state.tabs.find((t) => t.id === activeTabId);
          if (activeTab?.worktreeId === worktreeId) {
            activeTabId = tabs.length > 0 ? tabs[tabs.length - 1].id : null;
          }

          return { tabs, activeTabId };
        });

        logger.info("[BrowserStore] Closed all tabs for worktree", { worktreeId });
      },

      setActiveTab: (id: string) => {
        set({ activeTabId: id });
        logger.debug("[BrowserStore] Set active tab", { id });
      },

      getActiveTab: () => {
        const state = get();
        if (!state.activeTabId) return null;
        return state.tabs.find((t) => t.id === state.activeTabId) || null;
      },

      getProjectTabs: (projectId?: string) => {
        const state = get();
        if (!projectId) return state.tabs;
        return state.tabs.filter((t) => t.projectId === projectId);
      },

      getWorktreeTabs: (worktreeId: string) => {
        const state = get();
        return state.tabs.filter((t) => t.worktreeId === worktreeId);
      },

      updateTabUrl: (id: string, url: string) => {
        set((state) => ({
          tabs: state.tabs.map((t) => (t.id === id ? { ...t, url } : t)),
        }));
        logger.debug("[BrowserStore] Updated tab URL", { id, url });
      },

      updateTabTitle: (id: string, title: string) => {
        set((state) => ({
          tabs: state.tabs.map((t) => (t.id === id ? { ...t, title } : t)),
        }));
        logger.debug("[BrowserStore] Updated tab title", { id, title });
      },

      updateTabFavicon: (id: string, favicon: string) => {
        set((state) => ({
          tabs: state.tabs.map((t) => (t.id === id ? { ...t, favicon } : t)),
        }));
        logger.debug("[BrowserStore] Updated tab favicon", { id, favicon });
      },

      updateTabLoading: (id: string, isLoading: boolean) => {
        set((state) => ({
          tabs: state.tabs.map((t) => (t.id === id ? { ...t, isLoading } : t)),
        }));
        logger.debug("[BrowserStore] Updated tab loading state", { id, isLoading });
      },

      updateTabNavigation: (id: string, canGoBack: boolean, canGoForward: boolean) => {
        set((state) => ({
          tabs: state.tabs.map((t) =>
            t.id === id ? { ...t, canGoBack, canGoForward } : t
          ),
        }));
        logger.debug("[BrowserStore] Updated tab navigation state", {
          id,
          canGoBack,
          canGoForward,
        });
      },

      updateTabPane: (id: string, paneId: string) => {
        set((state) => ({
          tabs: state.tabs.map((t) =>
            t.id === id ? { ...t, paneId } : t
          ),
        }));
        logger.info("[BrowserStore] Updated tab pane", { id, paneId });
      },

      navigateTab: (id: string, url: string) => {
        logger.info("[BrowserStore] Navigate tab", { id, url });

        // Update local state
        set((state) => ({
          tabs: state.tabs.map((t) =>
            t.id === id ? { ...t, url, isLoading: true } : t
          ),
        }));
      },

      goBack: (id: string) => {
        logger.info("[BrowserStore] Go back", { id });
        // Navigation is handled by webview element directly
      },

      goForward: (id: string) => {
        logger.info("[BrowserStore] Go forward", { id });
        // Navigation is handled by webview element directly
      },

      reload: (id: string) => {
        logger.info("[BrowserStore] Reload tab", { id });

        // Update loading state
        set((state) => ({
          tabs: state.tabs.map((t) => (t.id === id ? { ...t, isLoading: true } : t)),
        }));
        // Reload is handled by webview element directly
      },

    }),
    {
      name: "browser-storage",
      version: 2, // Bumped for worktreeId migration
      partialize: (state) => ({
        tabs: state.tabs,
        activeTabId: state.activeTabId,
      }),
      migrate: (persistedState: any, version: number) => {
        // Migration from version 1: Clear tabs without worktreeId
        // Since worktreeId is now required, old tabs without it must be removed
        if (version < 2 && persistedState?.tabs) {
          logger.info("[BrowserStore] Migrating to v2: clearing tabs without worktreeId");
          persistedState.tabs = persistedState.tabs.filter((tab: any) => tab.worktreeId);
          persistedState.activeTabId = persistedState.tabs.length > 0 
            ? persistedState.tabs[persistedState.tabs.length - 1].id 
            : null;
        }
        
        // Clean up Google URLs with zx parameters that cause infinite reload
        if (persistedState?.tabs) {
          persistedState.tabs = persistedState.tabs.map((tab: BrowserTab) => {
            if (tab.url.includes('google.com') && tab.url.includes('zx=')) {
              return { ...tab, url: tab.url.split('?')[0] };
            }
            return tab;
          });
        }
        return persistedState as PersistedBrowserState;
      },
    }
  )
);
