/**
 * SettingsService reads that components render — preferences and provider
 * statuses — on the generated hooks and keys.
 *
 * Each cache entry holds exactly what the generated hook fetches (a
 * `GetPreferencesResponse` under `settingsServiceKeys.getPreferences({})`, …)
 * and is converted on read. The imperative side — api/settings-grpc.ts, which
 * the zustand settings stores still call — reads through the SAME entries
 * (fetchPreferences / fetchProviderStatuses below) and invalidates them after
 * a write, so a store and a mounted hook can never show two answers.
 */

import { useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";

import type { MessageInitShape } from "@bufbuild/protobuf";

import {
  SettingsService,
  type GetPreferencesResponse,
  type GetProviderStatusesResponse,
  type UpdatePreferencesRequestSchema,
} from "../gen/reliant/v1/settings_pb";
import { connectClient } from "../lib/connect";
import { queryClient } from "../lib/query-client";
import { useAuthStore } from "@/store/authStore";
import {
  settingsServiceKeys,
  useGetPreferences,
  useGetProviderStatuses,
  useUpdatePreferences as useUpdatePreferencesRpc,
} from "./settings-service-hooks_gen";

const client = connectClient(SettingsService);

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

// --- Provider statuses ---

export const providerStatusesKey = () => settingsServiceKeys.getProviderStatuses({});

/** What "is a provider configured?" needs from a status row. */
export interface ProviderStatusLike {
  configured: boolean;
  hasApiKey?: boolean;
}

/** True once any provider has either a stored key or is otherwise configured (e.g. the managed `reliant` provider). */
export function hasAnyConfiguredProvider(providers: readonly ProviderStatusLike[]): boolean {
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
  return useGetProviderStatuses(
    {},
    {
      staleTime: 10_000,
      // GetProviderStatuses is an authenticated RPC. Without this gate it fired
      // on mount regardless of session — including on the sign-in screen, where
      // it produced a run of 401s against prod while the user was still moving
      // from email entry to code entry.
      enabled: hasSession,
    },
  );
}

/**
 * GetProviderStatuses for an imperative caller, through the hook's cache
 * entry: concurrent callers share one RPC, and the answer lands where
 * useProviderStatuses and the onboarding checklist read it.
 */
export function fetchProviderStatuses(staleTime = 0): Promise<GetProviderStatusesResponse> {
  return queryClient.fetchQuery({
    queryKey: providerStatusesKey(),
    queryFn: () => client.getProviderStatuses({}),
    staleTime,
  });
}

/** A provider credential changed (a sign-in, a key, a sync): refetch the statuses. */
export function invalidateProviderStatuses(): void {
  void queryClient.invalidateQueries({ queryKey: providerStatusesKey() });
}

// --- Preferences ---

// Types (matching settingsStore)
export type WorktreeArchiveMode = "ask_me" | "always_cleanup" | "always_keep";

export interface WorktreePreferences {
  archiveMode: WorktreeArchiveMode;
  defaultDeleteBranch: boolean;
  branchCopyUncommittedFilesDefault: boolean;
}

export interface UserPreferences {
  streamingEnabled: boolean;
  worktree: WorktreePreferences;
  skipDeleteConfirmation: boolean;
}

const SKIP_DELETE_CONFIRMATION = "skip_delete_confirmation";

export const preferencesKey = () => settingsServiceKeys.getPreferences({});

const DEFAULT_PREFERENCES: UserPreferences = {
  streamingEnabled: true,
  worktree: {
    archiveMode: "ask_me",
    defaultDeleteBranch: false,
    branchCopyUncommittedFilesDefault: false,
  },
  skipDeleteConfirmation: false,
};

/** The UI's view of a GetPreferences answer; the defaults when there is none. */
export function userPreferencesOf(response: GetPreferencesResponse | undefined): UserPreferences {
  if (!response) return DEFAULT_PREFERENCES;
  return {
    streamingEnabled: response.streamingEnabled,
    worktree: {
      archiveMode: (response.worktreeArchiveMode || "ask_me") as WorktreeArchiveMode,
      defaultDeleteBranch: response.worktreeDefaultDeleteBranch,
      branchCopyUncommittedFilesDefault: response.branchCopyUncommittedFilesDefault,
    },
    skipDeleteConfirmation: response.additional?.[SKIP_DELETE_CONFIRMATION] === "true",
  };
}

/**
 * The user's preferences. Signed out this serves the defaults rather than
 * `undefined`, so the modals that destructure `preferences.worktree` degrade
 * to defaults instead of throwing — and no RPC goes out.
 */
export function usePreferences(): { data: UserPreferences; isPending: boolean } {
  const hasSession = useHasSession();
  const q = useGetPreferences({}, { staleTime: 60_000, enabled: hasSession });
  const data = useMemo(() => userPreferencesOf(q.data), [q.data]);
  return { data, isPending: q.isPending };
}

/** The current preferences for an imperative caller (no fetch). */
export function cachedUserPreferences(): UserPreferences {
  return userPreferencesOf(queryClient.getQueryData<GetPreferencesResponse>(preferencesKey()));
}

/**
 * GetPreferences for an imperative caller, through the hook's cache entry:
 * concurrent callers share one RPC and the answer refreshes every mounted
 * usePreferences.
 */
export function fetchPreferences(): Promise<GetPreferencesResponse> {
  return queryClient.fetchQuery({
    queryKey: preferencesKey(),
    queryFn: () => client.getPreferences({}),
    staleTime: 0,
  });
}

/** Preferences were written outside a hook: refetch them. */
export function invalidatePreferences(): void {
  void queryClient.invalidateQueries({ queryKey: preferencesKey() });
}

type UpdatePreferencesInit = MessageInitShape<typeof UpdatePreferencesRequestSchema>;

function toUpdateRequest(prefs: Partial<UserPreferences>): UpdatePreferencesInit {
  const req: UpdatePreferencesInit = { additional: {} };
  if (prefs.streamingEnabled !== undefined) req.streamingEnabled = prefs.streamingEnabled;
  if (prefs.skipDeleteConfirmation !== undefined) {
    req.additional = { [SKIP_DELETE_CONFIRMATION]: String(prefs.skipDeleteConfirmation) };
  }
  if (prefs.worktree) {
    if (prefs.worktree.archiveMode !== undefined) req.worktreeArchiveMode = prefs.worktree.archiveMode;
    if (prefs.worktree.defaultDeleteBranch !== undefined) {
      req.worktreeDefaultDeleteBranch = prefs.worktree.defaultDeleteBranch;
    }
    if (prefs.worktree.branchCopyUncommittedFilesDefault !== undefined) {
      req.branchCopyUncommittedFilesDefault = prefs.worktree.branchCopyUncommittedFilesDefault;
    }
  }
  return req;
}

/** The cached GetPreferences answer with an update applied (the fields toUpdateRequest sets). */
function applyUpdate(prev: GetPreferencesResponse, req: UpdatePreferencesInit): GetPreferencesResponse {
  return {
    ...prev,
    streamingEnabled: req.streamingEnabled ?? prev.streamingEnabled,
    worktreeArchiveMode: req.worktreeArchiveMode ?? prev.worktreeArchiveMode,
    worktreeDefaultDeleteBranch: req.worktreeDefaultDeleteBranch ?? prev.worktreeDefaultDeleteBranch,
    branchCopyUncommittedFilesDefault:
      req.branchCopyUncommittedFilesDefault ?? prev.branchCopyUncommittedFilesDefault,
    additional: { ...prev.additional, ...req.additional },
  };
}

/**
 * Update some preferences. The cached answer changes at once and rolls back
 * if the server refuses; being the generated mutation, success invalidates
 * settingsServiceKeys.all, so every reader converges on what was stored.
 */
export function useUpdatePreferences() {
  const qc = useQueryClient();
  const rpc = useUpdatePreferencesRpc({
    onMutate: async (req) => {
      await qc.cancelQueries({ queryKey: preferencesKey() });
      const previous = qc.getQueryData<GetPreferencesResponse>(preferencesKey());
      if (previous) qc.setQueryData(preferencesKey(), applyUpdate(previous, req));
      return { previous };
    },
    // onMutate's return reaches onError typed (the generated hooks infer
    // TanStack's TContext from onMutate).
    onError: (_err, _req, context) => {
      if (context?.previous) qc.setQueryData(preferencesKey(), context.previous);
    },
  });
  return {
    ...rpc,
    mutate: (prefs: Partial<UserPreferences>) => rpc.mutate(toUpdateRequest(prefs)),
    mutateAsync: (prefs: Partial<UserPreferences>) => rpc.mutateAsync(toUpdateRequest(prefs)),
  };
}

export function useUpdateWorktreePreferences() {
  const updatePrefs = useUpdatePreferences();
  return {
    ...updatePrefs,
    mutate: (worktreePrefs: Partial<WorktreePreferences>) =>
      updatePrefs.mutate({ worktree: { ...cachedUserPreferences().worktree, ...worktreePrefs } }),
    mutateAsync: (worktreePrefs: Partial<WorktreePreferences>) =>
      updatePrefs.mutateAsync({ worktree: { ...cachedUserPreferences().worktree, ...worktreePrefs } }),
  };
}
