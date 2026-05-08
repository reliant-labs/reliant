import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";

// Types (matching settingsStore)
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
  skipDeleteConfirmation: boolean;
}

export const settingsKeys = {
  all: ["settings"] as const,
  preferences: () => [...settingsKeys.all, "preferences"] as const,
};

const DEFAULT_PREFERENCES: UserPreferences = {
  streamingEnabled: true,
  worktree: {
    archiveMode: "ask_me",
    defaultDeleteDirectory: true,
    defaultDeleteBranch: false,
    branchCopyUncommittedFilesDefault: false,
  },
  skipDeleteConfirmation: false,
};

function mapApiPreferences(apiPrefs: any): UserPreferences {
  return {
    streamingEnabled: apiPrefs.streaming_enabled ?? true,
    worktree: {
      archiveMode:
        (apiPrefs.worktree_archive_mode as WorktreeArchiveMode) ?? "ask_me",
      defaultDeleteDirectory:
        apiPrefs.worktree_default_delete_directory ?? true,
      defaultDeleteBranch:
        apiPrefs.worktree_default_delete_branch ?? false,
      branchCopyUncommittedFilesDefault:
        apiPrefs.branch_copy_uncommitted_files_default ?? false,
    },
    skipDeleteConfirmation:
      apiPrefs.additional?.["skip_delete_confirmation"] === "true",
  };
}

export function usePreferences() {
  return useQuery({
    queryKey: settingsKeys.preferences(),
    queryFn: async () => {
      const apiPrefs = await api.settings.getPreferences();
      return mapApiPreferences(apiPrefs);
    },
    placeholderData: DEFAULT_PREFERENCES,
    staleTime: 60_000,
  });
}

export function useUpdatePreferences() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (prefs: Partial<UserPreferences>) => {
      const apiPrefs: Record<string, unknown> = {};
      if (prefs.streamingEnabled !== undefined)
        apiPrefs.streaming_enabled = prefs.streamingEnabled;
      if (prefs.skipDeleteConfirmation !== undefined)
        apiPrefs.skip_delete_confirmation = prefs.skipDeleteConfirmation;
      if (prefs.worktree) {
        if (prefs.worktree.archiveMode !== undefined)
          apiPrefs.worktree_archive_mode = prefs.worktree.archiveMode;
        if (prefs.worktree.defaultDeleteDirectory !== undefined)
          apiPrefs.worktree_default_delete_directory =
            prefs.worktree.defaultDeleteDirectory;
        if (prefs.worktree.defaultDeleteBranch !== undefined)
          apiPrefs.worktree_default_delete_branch =
            prefs.worktree.defaultDeleteBranch;
        if (prefs.worktree.branchCopyUncommittedFilesDefault !== undefined)
          apiPrefs.branch_copy_uncommitted_files_default =
            prefs.worktree.branchCopyUncommittedFilesDefault;
      }
      return api.settings.updatePreferences(apiPrefs);
    },
    onMutate: async (newPrefs) => {
      await queryClient.cancelQueries({
        queryKey: settingsKeys.preferences(),
      });
      const previous = queryClient.getQueryData<UserPreferences>(
        settingsKeys.preferences(),
      );
      if (previous) {
        queryClient.setQueryData<UserPreferences>(
          settingsKeys.preferences(),
          {
            ...previous,
            ...newPrefs,
            worktree: { ...previous.worktree, ...newPrefs.worktree },
          },
        );
      }
      return { previous };
    },
    onError: (_err, _vars, context) => {
      if (context?.previous) {
        queryClient.setQueryData(
          settingsKeys.preferences(),
          context.previous,
        );
      }
    },
    onSettled: () => {
      queryClient.invalidateQueries({
        queryKey: settingsKeys.preferences(),
      });
    },
  });
}

export function useUpdateWorktreePreferences() {
  const queryClient = useQueryClient();
  const updatePrefs = useUpdatePreferences();
  return useMutation({
    mutationFn: async (worktreePrefs: Partial<WorktreePreferences>) => {
      const current = queryClient.getQueryData<UserPreferences>(
        settingsKeys.preferences(),
      );
      return updatePrefs.mutateAsync({
        worktree: {
          ...(current?.worktree ?? DEFAULT_PREFERENCES.worktree),
          ...worktreePrefs,
        },
      });
    },
  });
}
