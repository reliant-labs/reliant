import { create } from "zustand";

// Sort options for chat list
export type ChatSortOption =
  | "recent_activity" // by last_message_at (most recent first)
  | "needs_attention_first" // Chats needing attention at top, then by last_message_at
  | "newest_first" // Default: by created_at desc
  | "oldest_first" // by created_at asc
  | "alphabetical_asc" // by title A-Z
  | "alphabetical_desc"; // by title Z-A

// View mode options
export type ChatViewMode =
  | "grouped" // Grouped by workspace (default)
  | "flat"; // Flat list, no grouping

// Filter options (for future use)
export interface ChatFilters {
  states?: ("active" | "needs_attention" | "idle")[]; // Filter by chat state
}

interface ChatListPreferencesState {
  // Current preferences
  sortOrder: ChatSortOption;
  viewMode: ChatViewMode;
  filters: ChatFilters;

  // Actions
  setSortOrder: (sort: ChatSortOption) => void;
  setViewMode: (mode: ChatViewMode) => void;
  setFilters: (filters: ChatFilters) => void;
  resetFilters: () => void;
  resetAll: () => void;
}

const STORAGE_KEY = "chat-list-preferences";

// Load preferences from localStorage
function loadPreferences(): Partial<ChatListPreferencesState> {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved) {
      const parsed = JSON.parse(saved);
      return {
        sortOrder: parsed.sortOrder || "newest_first",
        viewMode: parsed.viewMode || "grouped",
        filters: parsed.filters || {},
      };
    }
  } catch (e) {
    console.warn("Failed to load chat list preferences:", e);
  }
  return {};
}

// Save preferences to localStorage
function savePreferences(state: {
  sortOrder: ChatSortOption;
  viewMode: ChatViewMode;
  filters: ChatFilters;
}) {
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        sortOrder: state.sortOrder,
        viewMode: state.viewMode,
        filters: state.filters,
      })
    );
  } catch (e) {
    console.warn("Failed to save chat list preferences:", e);
  }
}

const defaultFilters: ChatFilters = {};

export const useChatListPreferencesStore = create<ChatListPreferencesState>(
  (set, get) => {
    const loaded = loadPreferences();

    return {
      // Initial state from localStorage or defaults
      sortOrder: loaded.sortOrder || "newest_first",
      viewMode: loaded.viewMode || "grouped",
      filters: loaded.filters || defaultFilters,

      setSortOrder: (sortOrder) => {
        set({ sortOrder });
        savePreferences({ ...get(), sortOrder });
      },

      setViewMode: (viewMode) => {
        set({ viewMode });
        savePreferences({ ...get(), viewMode });
      },

      setFilters: (filters) => {
        set({ filters });
        savePreferences({ ...get(), filters });
      },

      resetFilters: () => {
        set({ filters: defaultFilters });
        savePreferences({ ...get(), filters: defaultFilters });
      },

      resetAll: () => {
        const defaults = {
          sortOrder: "newest_first" as ChatSortOption,
          viewMode: "grouped" as ChatViewMode,
          filters: defaultFilters,
        };
        set(defaults);
        savePreferences(defaults);
      },
    };
  }
);

// Selector hooks for optimal re-renders
export const useSortOrder = () =>
  useChatListPreferencesStore((state) => state.sortOrder);
export const useViewMode = () =>
  useChatListPreferencesStore((state) => state.viewMode);
export const useFilters = () =>
  useChatListPreferencesStore((state) => state.filters);