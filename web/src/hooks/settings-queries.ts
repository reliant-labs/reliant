import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { useAuthStore } from "@/store/authStore";

/**
 * Whether an outbound RPC can carry a credential.
 *
 * Reads `session`, not `user`. The two are not the same fact: the store can
 * hold a user while the session write is still in flight (under Electron that
 * is an IPC round-trip — see AuthInitializer.waitForAccessToken), and every
 * RPC issued in that window goes out with no Authorization header and comes
 * back "missing authorization token". Gating on `user` would leave that window
 * open, which is most of the bug.
 */
function useHasSession(): boolean {
  return useAuthStore((state) => !!state.session);
}

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
  providers: () => [...settingsKeys.all, "providers"] as const,
};

export type ProviderStatus = Awaited<ReturnType<typeof api.settings.getProviders>>[number];

/** True once any provider has either a stored key or is otherwise configured (e.g. the managed `reliant` provider). */
export function hasAnyConfiguredProvider(providers: ProviderStatus[]): boolean {
  return providers.some((p) => p.hasApiKey || p.configured);
}

/**
 * The single source of truth for "is any AI provider configured?" — server
 * state via `GetProviderStatuses`, not a latch set by whichever UI flow
 * happened to save a key. Every caller that needs this answer reads this
 * query (or `hasAnyConfiguredProvider` on its data) instead of listening for
 * an event; a forgotten emit site can no longer leave the answer stale
 * forever, only until the next refetch.
 */
export function useProviderStatuses() {
  const hasSession = useHasSession();
  return useQuery({
    queryKey: settingsKeys.providers(),
    queryFn: () => api.settings.getProviders(),
    staleTime: 10_000,
    // GetProviderStatuses is an authenticated RPC. Without this gate it fired
    // on mount regardless of session — including on the sign-in screen, where
    // it produced a run of 401s against prod while the user was still moving
    // from email entry to code entry.
    enabled: hasSession,
  });
}

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
  const hasSession = useHasSession();
  return useQuery({
    queryKey: settingsKeys.preferences(),
    queryFn: async () => {
      const apiPrefs = await api.settings.getPreferences();
      return mapApiPreferences(apiPrefs);
    },
    // Signed out this serves DEFAULT_PREFERENCES rather than `undefined`, so
    // the modals that destructure `preferences.worktree` degrade to defaults
    // instead of throwing. Gating a placeholder-backed query is therefore
    // free — no caller can tell the difference except that no RPC goes out.
    placeholderData: DEFAULT_PREFERENCES,
    staleTime: 60_000,
    enabled: hasSession,
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
