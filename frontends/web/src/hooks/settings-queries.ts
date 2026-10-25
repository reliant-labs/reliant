/**
 * SettingsService server state — preferences (worktree, config scope, default
 * workflow, hidden workflows/presets, browser bookmarks and link handling)
 * and provider statuses — on the generated hooks and keys. Privacy consent is
 * in ./privacy-queries (telemetry reads it, and must not import the auth
 * store this module gates on).
 *
 * Each cache entry holds exactly what the generated hook fetches (a
 * `GetPreferencesResponse` under `settingsServiceKeys.getPreferences({})`, …)
 * and every reader projects it with `select`; there is no zustand copy of any
 * of it. A write patches the entry at once and puts it back if the server
 * refuses (a switch that waits a round-trip reads as ignoring the click);
 * being the generated mutation, success invalidates `settingsServiceKeys.all`,
 * and the stream's `config_health` invalidates the same root
 * (store/streamInvalidatedScopes `settings`), so another device's change
 * reaches every reader too.
 *
 * Non-React readers (the browser tab default, link handling) read the same
 * entry through the imperative functions below.
 */

import { useMemo } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";

import type { MessageInitShape } from "@bufbuild/protobuf";

import { ConfigScope } from "../gen/reliant/v1/common_pb";
import {
  HiddenItemType,
  type GetPreferencesResponse,
  type GetProviderStatusesResponse,
  type UpdatePreferencesRequestSchema,
} from "../gen/reliant/v1/settings_pb";
import { logger } from "../lib/logger";
import { queryClient } from "../lib/query-client";
import { useAuthStore } from "@/store/authStore";
import {
  settingsServiceKeys,
  settingsServiceQueries,
  useGetPreferences,
  useGetProviderStatuses,
  useSetHiddenItem as useSetHiddenItemRpc,
  useUpdatePreferences as useUpdatePreferencesRpc,
} from "./settings-service-hooks_gen";

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
  return queryClient.fetchQuery(settingsServiceQueries.getProviderStatuses({}, { staleTime }));
}

/** A provider credential changed (a sign-in, a key, a sync): refetch the statuses. */
export function invalidateProviderStatuses(): void {
  void queryClient.invalidateQueries({ queryKey: providerStatusesKey() });
}

// --- Preferences ---

// Types
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

/** The cached GetPreferences answer with an update applied (every field UpdatePreferences writes). */
function applyUpdate(prev: GetPreferencesResponse, req: UpdatePreferencesInit): GetPreferencesResponse {
  return {
    ...prev,
    streamingEnabled: req.streamingEnabled ?? prev.streamingEnabled,
    worktreeArchiveMode: req.worktreeArchiveMode ?? prev.worktreeArchiveMode,
    worktreeDefaultDeleteBranch: req.worktreeDefaultDeleteBranch ?? prev.worktreeDefaultDeleteBranch,
    branchCopyUncommittedFilesDefault:
      req.branchCopyUncommittedFilesDefault ?? prev.branchCopyUncommittedFilesDefault,
    defaultMcpScope: req.defaultMcpScope ?? prev.defaultMcpScope,
    defaultWorkflowScope: req.defaultWorkflowScope ?? prev.defaultWorkflowScope,
    defaultWorkflow: req.defaultWorkflow ?? prev.defaultWorkflow,
    hideBuiltinWorkflows: req.hideBuiltinWorkflows ?? prev.hideBuiltinWorkflows,
    hideBuiltinPresets: req.hideBuiltinPresets ?? prev.hideBuiltinPresets,
    additional: { ...prev.additional, ...req.additional },
  };
}

interface PreferencesSnapshot {
  previous: GetPreferencesResponse | undefined;
}

/** Patch the cached preferences at once; returns what was there, to put back. */
async function patchPreferences(
  qc: QueryClient,
  patch: (prev: GetPreferencesResponse) => GetPreferencesResponse,
): Promise<PreferencesSnapshot> {
  await qc.cancelQueries({ queryKey: preferencesKey() });
  const previous = qc.getQueryData<GetPreferencesResponse>(preferencesKey());
  if (previous) qc.setQueryData(preferencesKey(), patch(previous));
  return { previous };
}

function restorePreferences(qc: QueryClient, snapshot: PreferencesSnapshot | undefined): void {
  if (snapshot?.previous) qc.setQueryData(preferencesKey(), snapshot.previous);
}

/**
 * The cached preferences, fetched first when nothing is cached — for a write
 * that rewrites a whole value (the bookmark list) and must start from the
 * stored one, not from an empty default.
 */
function preferencesForWrite(qc: QueryClient): Promise<GetPreferencesResponse> {
  return qc.ensureQueryData(settingsServiceQueries.getPreferences({}));
}

/**
 * UpdatePreferences with the request as the RPC takes it. The cached answer
 * changes at once and rolls back if the server refuses; being the generated
 * mutation, success invalidates settingsServiceKeys.all, so every reader
 * converges on what was stored.
 */
function usePreferencesWrite() {
  const qc = useQueryClient();
  return useUpdatePreferencesRpc({
    onMutate: (req) => patchPreferences(qc, (prev) => applyUpdate(prev, req)),
    // onMutate's return reaches onError typed (the generated hooks infer
    // TanStack's TContext from onMutate).
    onError: (_err, _req, context) => restorePreferences(qc, context),
  });
}

/** Update some of the UI preferences (streaming, worktree, delete confirmation). */
export function useUpdatePreferences() {
  const rpc = usePreferencesWrite();
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

// --- Config scope + default workflow ---

/** The workflow a new chat runs when the user has picked none (the backend's default too). */
export const DEFAULT_WORKFLOW = "builtin://agent";

export interface ScopePreferences {
  defaultMcpScope: ConfigScope;
  defaultWorkflowScope: ConfigScope;
  defaultWorkflow: string;
  hideBuiltinWorkflows: boolean;
  hideBuiltinPresets: boolean;
}

const DEFAULT_SCOPE_PREFERENCES: ScopePreferences = {
  defaultMcpScope: ConfigScope.PROJECT,
  defaultWorkflowScope: ConfigScope.PROJECT,
  defaultWorkflow: DEFAULT_WORKFLOW,
  hideBuiltinWorkflows: false,
  hideBuiltinPresets: false,
};

function scopePreferencesOf(response: GetPreferencesResponse): ScopePreferences {
  return {
    // UNSPECIFIED means project scope.
    defaultMcpScope: response.defaultMcpScope || ConfigScope.PROJECT,
    defaultWorkflowScope: response.defaultWorkflowScope || ConfigScope.PROJECT,
    defaultWorkflow: response.defaultWorkflow || DEFAULT_WORKFLOW,
    hideBuiltinWorkflows: response.hideBuiltinWorkflows,
    hideBuiltinPresets: response.hideBuiltinPresets,
  };
}

/**
 * Where new MCP servers and workflows are saved, the default workflow, and
 * whether builtins are hidden. The defaults until the answer arrives (and
 * signed out); `isPending` says which.
 */
export function useScopePreferences(): { data: ScopePreferences; isPending: boolean } {
  const hasSession = useHasSession();
  const q = useGetPreferences({}, { staleTime: 60_000, enabled: hasSession, select: scopePreferencesOf });
  return { data: q.data ?? DEFAULT_SCOPE_PREFERENCES, isPending: q.isPending };
}

/** The workflow a new chat runs: the user's default, else DEFAULT_WORKFLOW. */
export function useDefaultWorkflow(): string {
  return useScopePreferences().data.defaultWorkflow;
}

/** Change the config-scope preferences or the default workflow: `mutateAsync({ defaultWorkflow })`. */
export function useUpdateScopePreferences() {
  const rpc = usePreferencesWrite();
  const { mutateAsync } = rpc;
  const update = useMemo(
    () => (prefs: Partial<ScopePreferences>) =>
      mutateAsync({
        defaultMcpScope: prefs.defaultMcpScope,
        defaultWorkflowScope: prefs.defaultWorkflowScope,
        defaultWorkflow: prefs.defaultWorkflow,
        hideBuiltinWorkflows: prefs.hideBuiltinWorkflows,
        hideBuiltinPresets: prefs.hideBuiltinPresets,
      }).then(() => undefined),
    [mutateAsync],
  );
  return { isPending: rpc.isPending, mutateAsync: update };
}

// --- Hidden workflows and presets ---

export interface HiddenItems {
  isWorkflowHidden: (slug: string) => boolean;
  isPresetHidden: (slug: string) => boolean;
}

const NOTHING_HIDDEN: HiddenItems = { isWorkflowHidden: () => false, isPresetHidden: () => false };

function hiddenItemsOf(response: GetPreferencesResponse): HiddenItems {
  const workflows = new Set(response.hiddenWorkflowSlugs);
  const presets = new Set(response.hiddenPresetSlugs);
  return { isWorkflowHidden: (slug) => workflows.has(slug), isPresetHidden: (slug) => presets.has(slug) };
}

/**
 * Which builtin workflows and presets the user hid from the pickers. Nothing
 * is hidden until the answer arrives. The predicates change identity when the
 * hidden set does, so a memo keyed on them recomputes.
 */
export function useHiddenItems(): HiddenItems {
  const hasSession = useHasSession();
  const q = useGetPreferences({}, { staleTime: 60_000, enabled: hasSession, select: hiddenItemsOf });
  return q.data ?? NOTHING_HIDDEN;
}

function hiddenSlugsField(itemType: HiddenItemType | undefined) {
  return itemType === HiddenItemType.PRESET ? "hiddenPresetSlugs" : "hiddenWorkflowSlugs";
}

function withHidden(
  prev: GetPreferencesResponse,
  itemType: HiddenItemType | undefined,
  slug: string,
  hidden: boolean,
): GetPreferencesResponse {
  const field = hiddenSlugsField(itemType);
  const rest = prev[field].filter((s) => s !== slug);
  return { ...prev, [field]: hidden ? [...rest, slug] : rest };
}

/**
 * Show or hide a workflow / preset in the pickers. It moves at once and comes
 * back if the server refuses; the returned promise rejects then, for the
 * caller's toast.
 */
export function useToggleHiddenItem() {
  const qc = useQueryClient();
  const rpc = useSetHiddenItemRpc({
    onMutate: ({ itemType, slug, hidden }) =>
      patchPreferences(qc, (prev) => withHidden(prev, itemType, slug ?? "", hidden ?? false)),
    onError: (_err, _req, context) => restorePreferences(qc, context),
  });
  const { mutateAsync } = rpc;
  return useMemo(() => {
    const toggle = (itemType: HiddenItemType) => async (slug: string) => {
      const prefs = qc.getQueryData<GetPreferencesResponse>(preferencesKey());
      const hidden = !(prefs?.[hiddenSlugsField(itemType)] ?? []).includes(slug);
      await mutateAsync({ itemType, slug, hidden });
    };
    return {
      toggleWorkflowVisibility: toggle(HiddenItemType.WORKFLOW),
      togglePresetVisibility: toggle(HiddenItemType.PRESET),
    };
  }, [qc, mutateAsync]);
}

// --- Browser: default page, link handling, bookmarks (in `additional`) ---

export const BROWSER_DEFAULT_PAGE = "browserDefaultPage";
export const BROWSER_OPEN_LINKS_IN_APP = "browserOpenLinksInApp";
const BROWSER_BOOKMARKS = "browserBookmarks";

export const FALLBACK_BROWSER_PAGE = "https://www.google.com";

export interface BrowserPreferences {
  /** The page a new tab opens; empty when the user set none. */
  defaultPage: string;
  /** Whether links open in the embedded browser (the default) or the system one. */
  openLinksInApp: boolean;
}

function browserPreferencesOf(response: GetPreferencesResponse | undefined): BrowserPreferences {
  return {
    defaultPage: response?.additional[BROWSER_DEFAULT_PAGE] ?? "",
    openLinksInApp: response?.additional[BROWSER_OPEN_LINKS_IN_APP] !== "false",
  };
}

export function useBrowserPreferences(): { data: BrowserPreferences; isPending: boolean } {
  const hasSession = useHasSession();
  const q = useGetPreferences({}, { staleTime: 60_000, enabled: hasSession, select: browserPreferencesOf });
  return { data: q.data ?? browserPreferencesOf(undefined), isPending: q.isPending };
}

/**
 * The browser preferences for a non-React caller (a new tab, a link click).
 * A fresh cached answer is used as is; otherwise this fetches (and rejects
 * if that fails — each caller has its own fallback).
 */
export async function readBrowserPreferences(): Promise<BrowserPreferences> {
  return browserPreferencesOf(
    await queryClient.fetchQuery(settingsServiceQueries.getPreferences({}, { staleTime: 60_000 })),
  );
}

export function useUpdateBrowserPreferences() {
  const rpc = usePreferencesWrite();
  const { mutateAsync } = rpc;
  const update = useMemo(
    () => (prefs: Partial<BrowserPreferences>) => {
      const additional: Record<string, string> = {};
      if (prefs.defaultPage !== undefined) additional[BROWSER_DEFAULT_PAGE] = prefs.defaultPage;
      if (prefs.openLinksInApp !== undefined) additional[BROWSER_OPEN_LINKS_IN_APP] = String(prefs.openLinksInApp);
      return mutateAsync({ additional }).then(() => undefined);
    },
    [mutateAsync],
  );
  return { isPending: rpc.isPending, mutateAsync: update };
}

export interface Bookmark {
  id: string;
  title: string;
  url: string;
  favicon?: string;
  createdAt: string;
}

const NO_BOOKMARKS: Bookmark[] = [];

function bookmarksOf(response: GetPreferencesResponse | undefined): Bookmark[] {
  const json = response?.additional[BROWSER_BOOKMARKS];
  if (!json) return NO_BOOKMARKS;
  try {
    const parsed: unknown = JSON.parse(json);
    return Array.isArray(parsed) ? (parsed as Bookmark[]) : NO_BOOKMARKS;
  } catch (error) {
    logger.error("[settings] stored bookmarks are not JSON:", error);
    return NO_BOOKMARKS;
  }
}

/** The user's browser bookmarks (none until the answer arrives). */
export function useBookmarks(): Bookmark[] {
  const hasSession = useHasSession();
  const q = useGetPreferences({}, { staleTime: 60_000, enabled: hasSession, select: bookmarksOf });
  return q.data ?? NO_BOOKMARKS;
}

/**
 * Add or remove a bookmark. The list is one stored value, so each write
 * starts from the stored list (fetched first if nothing is cached — never an
 * empty default, which would overwrite it). The sidebar shows the change at
 * once and puts the list back if the server refuses; the promise rejects
 * then, for the caller's toast.
 */
export function useBookmarkActions() {
  const qc = useQueryClient();
  const rpc = usePreferencesWrite();
  const { mutateAsync } = rpc;
  return useMemo(() => {
    const write = (bookmarks: Bookmark[]) =>
      mutateAsync({ additional: { [BROWSER_BOOKMARKS]: JSON.stringify(bookmarks) } }).then(() => undefined);
    return {
      addBookmark: async (bookmark: Omit<Bookmark, "id" | "createdAt">) => {
        const current = bookmarksOf(await preferencesForWrite(qc));
        if (current.some((b) => b.url === bookmark.url)) {
          logger.warn("[settings] URL already bookmarked:", bookmark.url);
          return;
        }
        await write([
          ...current,
          {
            ...bookmark,
            id: `bookmark_${Date.now()}_${Math.random().toString(36).slice(2, 11)}`,
            createdAt: new Date().toISOString(),
          },
        ]);
      },
      removeBookmark: async (id: string) => {
        const current = bookmarksOf(await preferencesForWrite(qc));
        await write(current.filter((b) => b.id !== id));
      },
    };
  }, [qc, mutateAsync]);
}
