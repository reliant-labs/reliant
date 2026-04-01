import { create } from "zustand";
import { persist } from "zustand/middleware";
import { api } from "../api/client";
import { logger } from "../lib/logger";

export interface Bookmark {
  id: string;
  title: string;
  url: string;
  favicon?: string;
  createdAt: string;
}

interface BookmarksState {
  bookmarks: Bookmark[];
  isLoading: boolean;
  error: string | null;

  // Actions
  loadBookmarks: () => Promise<void>;
  addBookmark: (bookmark: Omit<Bookmark, "id" | "createdAt">) => Promise<void>;
  removeBookmark: (id: string) => Promise<void>;
  updateBookmark: (id: string, updates: Partial<Omit<Bookmark, "id" | "createdAt">>) => Promise<void>;
}

const BOOKMARKS_PREFERENCE_KEY = "browserBookmarks";

export const useBookmarksStore = create<BookmarksState>()(
  persist(
    (set, get) => ({
      bookmarks: [],
      isLoading: false,
      error: null,

      loadBookmarks: async () => {
        set({ isLoading: true, error: null });
        try {
          const preferences = await api.settings.getPreferences();
          // The API spreads `additional` fields at the top level
          const bookmarksJson = (preferences as unknown as Record<string, unknown>)[BOOKMARKS_PREFERENCE_KEY] as string | undefined;
          
          if (bookmarksJson) {
            try {
              const bookmarks = JSON.parse(bookmarksJson) as Bookmark[];
              set({ bookmarks, isLoading: false });
              logger.debug("[BookmarksStore] Loaded bookmarks:", bookmarks.length);
            } catch (parseError) {
              logger.error("[BookmarksStore] Failed to parse bookmarks:", parseError);
              set({ bookmarks: [], isLoading: false });
            }
          } else {
            set({ bookmarks: [], isLoading: false });
          }
        } catch (error) {
          logger.error("[BookmarksStore] Failed to load bookmarks:", error);
          set({
            error: error instanceof Error ? error.message : "Failed to load bookmarks",
            isLoading: false,
          });
        }
      },

      addBookmark: async (bookmark) => {
        const currentBookmarks = get().bookmarks;
        
        // Check if URL already bookmarked
        if (currentBookmarks.some(b => b.url === bookmark.url)) {
          logger.warn("[BookmarksStore] URL already bookmarked:", bookmark.url);
          return;
        }

        const newBookmark: Bookmark = {
          ...bookmark,
          id: `bookmark_${Date.now()}_${Math.random().toString(36).substr(2, 9)}`,
          createdAt: new Date().toISOString(),
        };

        const newBookmarks = [...currentBookmarks, newBookmark];
        
        // Optimistic update
        set({ bookmarks: newBookmarks });

        try {
          await api.settings.updatePreferences({
            [BOOKMARKS_PREFERENCE_KEY]: JSON.stringify(newBookmarks),
          });
          logger.info("[BookmarksStore] Added bookmark:", newBookmark.title);
        } catch (error) {
          // Revert on error
          set({ bookmarks: currentBookmarks });
          logger.error("[BookmarksStore] Failed to add bookmark:", error);
          throw error;
        }
      },

      removeBookmark: async (id) => {
        const currentBookmarks = get().bookmarks;
        const newBookmarks = currentBookmarks.filter(b => b.id !== id);
        
        // Optimistic update
        set({ bookmarks: newBookmarks });

        try {
          await api.settings.updatePreferences({
            [BOOKMARKS_PREFERENCE_KEY]: JSON.stringify(newBookmarks),
          });
          logger.info("[BookmarksStore] Removed bookmark:", id);
        } catch (error) {
          // Revert on error
          set({ bookmarks: currentBookmarks });
          logger.error("[BookmarksStore] Failed to remove bookmark:", error);
          throw error;
        }
      },

      updateBookmark: async (id, updates) => {
        const currentBookmarks = get().bookmarks;
        const newBookmarks = currentBookmarks.map(b =>
          b.id === id ? { ...b, ...updates } : b
        );
        
        // Optimistic update
        set({ bookmarks: newBookmarks });

        try {
          await api.settings.updatePreferences({
            [BOOKMARKS_PREFERENCE_KEY]: JSON.stringify(newBookmarks),
          });
          logger.info("[BookmarksStore] Updated bookmark:", id);
        } catch (error) {
          // Revert on error
          set({ bookmarks: currentBookmarks });
          logger.error("[BookmarksStore] Failed to update bookmark:", error);
          throw error;
        }
      },
    }),
    {
      name: "reliant-bookmarks",
      // Persist bookmarks locally for offline access
      partialize: (state) => ({ bookmarks: state.bookmarks }),
    }
  )
);
