import { create } from "zustand";
import { persist } from "zustand/middleware";
import { api } from "../api/client";
import { toast } from "../lib/toast-manager";
import { logger } from "../lib/logger";

export type WorktreeArchiveMode = "ask_me" | "always_cleanup" | "always_keep";

export interface WorktreePreferences {
  archiveMode: WorktreeArchiveMode;
  defaultDeleteDirectory: boolean;
  defaultDeleteBranch: boolean;
  branchCopyUncommittedFilesDefault: boolean;
}

export interface UserPreferences {
  streamingEnabled: boolean;
  worktree: WorktreePreferences;
  skipDeleteConfirmation: boolean; // Skip delete confirmation dialog
}

interface SettingsStore {
  preferences: UserPreferences;
  isLoading: boolean;
  error: string | null;
  hasHydrated: boolean; // Track when localStorage rehydration is complete

  // Actions
  loadPreferences: () => Promise<void>;
  updateWorktreePreferences: (
    prefs: Partial<WorktreePreferences>
  ) => Promise<void>;
  updatePreferences: (prefs: Partial<UserPreferences>) => Promise<void>;
  setHasHydrated: (hydrated: boolean) => void;
  reset: () => void;
}

const DEFAULT_PREFERENCES: UserPreferences = {
  streamingEnabled: true,
  worktree: {
    archiveMode: "ask_me", // Safe default - always ask
    defaultDeleteDirectory: true, // When in always_cleanup mode
    defaultDeleteBranch: false, // Safer to not delete branches by default
    branchCopyUncommittedFilesDefault: false, // Safe default - don't copy uncommitted files
  },
  skipDeleteConfirmation: false, // Default to showing confirmation
};

export const useSettingsStore = create<SettingsStore>()(
  persist(
    (set, get) => ({
      preferences: DEFAULT_PREFERENCES,
      isLoading: false,
      error: null,
      hasHydrated: false,

      setHasHydrated: (hydrated: boolean) => set({ hasHydrated: hydrated }),

      loadPreferences: async () => {
        set({ isLoading: true, error: null });
        try {
          const response = await api.settings.getPreferences();

          // Map backend response to frontend structure
          const skipDeleteConfirmation = response.additional?.["skip_delete_confirmation"] === "true";
          
          const preferences: UserPreferences = {
            streamingEnabled: response.streaming_enabled ?? true,
            worktree: {
              archiveMode:
                (response.worktree_archive_mode as WorktreeArchiveMode) ??
                "ask_me",
              defaultDeleteDirectory:
                response.worktree_default_delete_directory ?? true,
              defaultDeleteBranch:
                response.worktree_default_delete_branch ?? false,
              branchCopyUncommittedFilesDefault:
                response.branch_copy_uncommitted_files_default ?? false,
            },
            skipDeleteConfirmation,
          };

          set({ preferences, isLoading: false });
        } catch (error) {
          logger.error("Failed to load preferences:", error);
          set({
            error:
              error instanceof Error
                ? error.message
                : "Failed to load preferences",
            isLoading: false,
          });
          // Keep using cached/default preferences on error
        }
      },

      updateWorktreePreferences: async (
        prefs: Partial<WorktreePreferences>
      ) => {
        const currentPrefs = get().preferences;
        const newWorktreePrefs = { ...currentPrefs.worktree, ...prefs };

        // Optimistic update
        set({
          preferences: {
            ...currentPrefs,
            worktree: newWorktreePrefs,
          },
        });

        try {
          // Map to backend format
          const backendPrefs: Record<string, unknown> = {};
          if (prefs.archiveMode !== undefined) {
            backendPrefs.worktree_archive_mode = prefs.archiveMode;
          }
          if (prefs.defaultDeleteDirectory !== undefined) {
            backendPrefs.worktree_default_delete_directory =
              prefs.defaultDeleteDirectory;
          }
          if (prefs.defaultDeleteBranch !== undefined) {
            backendPrefs.worktree_default_delete_branch =
              prefs.defaultDeleteBranch;
          }
          if (prefs.branchCopyUncommittedFilesDefault !== undefined) {
            backendPrefs.branch_copy_uncommitted_files_default =
              prefs.branchCopyUncommittedFilesDefault;
          }

          await api.settings.updatePreferences(backendPrefs);

          toast.success("Worktree preferences updated", { duration: 2000 });
        } catch (error) {
          // Revert on error
          set({ preferences: currentPrefs });
          toast.error("Failed to update preferences");
          logger.error("Failed to update worktree preferences:", error);
          throw error;
        }
      },

      updatePreferences: async (prefs: Partial<UserPreferences>) => {
        const currentPrefs = get().preferences;
        const newPrefs = { ...currentPrefs, ...prefs };

        // Optimistic update
        set({ preferences: newPrefs });

        try {
          // Map to backend format
          const backendPrefs: Record<string, unknown> = {};
          if (prefs.streamingEnabled !== undefined) {
            backendPrefs.streaming_enabled = prefs.streamingEnabled;
          }
          if (prefs.skipDeleteConfirmation !== undefined) {
            backendPrefs.skip_delete_confirmation = prefs.skipDeleteConfirmation;
          }
          if (prefs.worktree) {
            if (prefs.worktree.archiveMode !== undefined) {
              backendPrefs.worktree_archive_mode = prefs.worktree.archiveMode;
            }
            if (prefs.worktree.defaultDeleteDirectory !== undefined) {
              backendPrefs.worktree_default_delete_directory =
                prefs.worktree.defaultDeleteDirectory;
            }
            if (prefs.worktree.defaultDeleteBranch !== undefined) {
              backendPrefs.worktree_default_delete_branch =
                prefs.worktree.defaultDeleteBranch;
            }
            if (
              prefs.worktree.branchCopyUncommittedFilesDefault !== undefined
            ) {
              backendPrefs.branch_copy_uncommitted_files_default =
                prefs.worktree.branchCopyUncommittedFilesDefault;
            }
          }

          await api.settings.updatePreferences(backendPrefs);
        } catch (error) {
          // Revert on error
          set({ preferences: currentPrefs });
          toast.error("Failed to update preferences");
          logger.error("Failed to update preferences:", error);
          throw error;
        }
      },

      reset: () => {
        set({
          preferences: DEFAULT_PREFERENCES,
          isLoading: false,
          error: null,
          hasHydrated: false,
        });
      },
    }),
    {
      name: "reliant-settings",
      // Only persist preferences, not loading/error states
      partialize: (state) => ({ preferences: state.preferences }),
      // Merge persisted state with defaults to handle new fields
      merge: (persistedState, currentState) => {
        const persisted = persistedState as {
          preferences?: Partial<UserPreferences>;
        };
        return {
          ...currentState,
          preferences: {
            ...DEFAULT_PREFERENCES,
            ...persisted?.preferences,
            // Ensure nested worktree object is properly merged
            worktree: {
              ...DEFAULT_PREFERENCES.worktree,
              ...persisted?.preferences?.worktree,
            },
          },
        };
      },
      // Track when localStorage rehydration is complete
      onRehydrateStorage: () => (_state, error) => {
        if (error) {
          logger.error("Failed to rehydrate settings store:", error);
        }
        // Mark hydration as complete regardless of error
        // (we have DEFAULT_PREFERENCES as fallback)
        useSettingsStore.getState().setHasHydrated(true);
      },
    }
  )
);
