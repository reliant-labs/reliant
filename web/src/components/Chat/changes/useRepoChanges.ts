import { useCallback, useEffect, useRef, useState } from "react";
import * as gitApi from "../../../api/git";
import { isDaemonConnectingError } from "../../../lib/daemon-errors";
import { logger } from "../../../lib/logger";
import { matchesRefetchScope, subscribeToRefetch } from "../../../store/refetchStore";
import { fetchChanges, getCachedData, invalidateCache, setCachedData } from "./gitStatusCache";
import type { RecentChangesData } from "./types";

interface ScopeArgs {
  worktreeId?: string;
  projectId: string;
  repoId?: string;
}

function scopeKeyOf({ worktreeId, projectId, repoId }: ScopeArgs): string {
  return `${worktreeId ?? "project"}:${projectId}:${repoId ?? ""}`;
}

interface UseRepoChangesArgs extends ScopeArgs {
  /** Skip loading entirely (not a git repo, or the parent reported an error). */
  enabled: boolean;
  projectDefaultBranch: string;
  /** Called synchronously when the scope (worktree/project/repo) changes, so
   *  the caller can drop selection and other per-scope UI state. */
  onScopeChange?: () => void;
}

/**
 * Loads one repo's working-tree changes and keeps them fresh: backend
 * refetch events (debounced), a 30s fallback poll, and a 5s module cache.
 *
 * RACE PROTECTION — guarded by RecentChanges.race.test.tsx. Switching chats
 * changes the scope while a slow response for the previous scope is still in
 * flight. Every request captures a request id and its scope key; a response
 * is applied only if both still match. The scope-change effect bumps the id
 * and clears data synchronously, so a stale response can never paint the
 * previous worktree's files into the new one.
 */
export function useRepoChanges({
  worktreeId,
  projectId,
  repoId,
  enabled,
  projectDefaultBranch,
  onScopeChange,
}: UseRepoChangesArgs) {
  const [data, setData] = useState<RecentChangesData | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  // The machine isn't serving (asleep and being woken, or still starting):
  // not an error, a wait. The panel renders the shared machine-wait state and
  // retries on its cadence (useDaemonWait) instead of "Couldn't load changes".
  const [waitingOnDaemon, setWaitingOnDaemon] = useState(false);
  const [refreshing, setRefreshing] = useState(false);

  const loadRequestIdRef = useRef(0);
  const scopeKeyRef = useRef(scopeKeyOf({ worktreeId, projectId, repoId }));
  const prevScopeRef = useRef({ worktreeId, projectId, repoId });
  const onScopeChangeRef = useRef(onScopeChange);
  onScopeChangeRef.current = onScopeChange;

  // Workspace switched: invalidate in-flight requests and clear stale state
  // before anything else can render it.
  useEffect(() => {
    const prev = prevScopeRef.current;
    if (prev.worktreeId === worktreeId && prev.projectId === projectId && prev.repoId === repoId) return;

    logger.debug("[RecentChanges] Workspace changed", {
      prevWorktree: prev.worktreeId,
      newWorktree: worktreeId,
      prevProject: prev.projectId,
      newProject: projectId,
      prevRepo: prev.repoId,
      newRepo: repoId,
    });

    loadRequestIdRef.current += 1;
    scopeKeyRef.current = scopeKeyOf({ worktreeId, projectId, repoId });
    prevScopeRef.current = { worktreeId, projectId, repoId };

    setData(null);
    setLoading(true);
    setLoadError(null);
    setWaitingOnDaemon(false);
    onScopeChangeRef.current?.();
  }, [worktreeId, projectId, repoId]);

  const load = useCallback(
    async (isInitial: boolean) => {
      const requestId = ++loadRequestIdRef.current;
      const requestScopeKey = scopeKeyOf({ worktreeId, projectId, repoId });
      const isCurrent = () =>
        loadRequestIdRef.current === requestId && scopeKeyRef.current === requestScopeKey;

      try {
        setLoadError(null);
        if (isInitial) setLoading(true);

        const changesData = await fetchChanges(worktreeId, projectId, repoId, projectDefaultBranch);

        if (!isCurrent()) {
          logger.debug("[RecentChanges] Ignoring stale changes response", {
            requestId,
            currentRequestId: loadRequestIdRef.current,
            requestScopeKey,
            latestScopeKey: scopeKeyRef.current,
          });
          return;
        }

        setData(changesData);
        setWaitingOnDaemon(false);
        setCachedData(worktreeId, projectId, changesData, repoId);
      } catch (err) {
        if (!isCurrent()) return;
        if (isDaemonConnectingError(err)) {
          setWaitingOnDaemon(true);
          return;
        }
        setWaitingOnDaemon(false);
        const message = err instanceof Error ? err.message : "Failed to load changes";
        console.error("[RecentChanges] Failed to load recent changes:", {
          error: err,
          message,
          projectId,
          worktreeId,
          repoId,
        });
        setLoadError(message);
      } finally {
        if (isCurrent()) setLoading(false);
      }
    },
    [worktreeId, projectId, repoId, projectDefaultBranch],
  );

  // Keep a stable handle for subscriptions so they always call the latest
  // closure without resubscribing on every render.
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      return;
    }

    const cached = getCachedData(worktreeId, projectId, repoId);
    if (cached) {
      setData(cached);
      setLoading(false);
      setLoadError(null);
    } else {
      setLoading(true);
      setLoadError(null);
      void loadRef.current(true);
    }

    // Fast path: the backend signals after tool calls. Debounced so a burst
    // of tool completions collapses into a single fetch.
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;
    const unsubscribe = subscribeToRefetch("worktree_changes", (event) => {
      if (!matchesRefetchScope(event, { worktreeId, projectId })) return;
      if (debounceTimer) clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => {
        debounceTimer = null;
        void loadRef.current(false);
      }, 500);
    });

    // Slow fallback poll for edits made outside Reliant (another editor, a
    // git pull in a terminal, …).
    const fallbackInterval = setInterval(() => {
      void loadRef.current(false);
    }, 30_000);

    return () => {
      unsubscribe();
      clearInterval(fallbackInterval);
      if (debounceTimer) clearTimeout(debounceTimer);
    };
  }, [worktreeId, projectId, repoId, enabled]);

  /** Drop the cache and refetch — call after any git write. */
  const reload = useCallback(async () => {
    invalidateCache(worktreeId, projectId, repoId);
    await loadRef.current(false);
  }, [worktreeId, projectId, repoId]);

  /** User-initiated refresh: same as reload, with a visible spinner. */
  const refresh = useCallback(async () => {
    setRefreshing(true);
    try {
      await reload();
    } finally {
      setRefreshing(false);
    }
  }, [reload]);

  /** Retry after a failed initial load. */
  const retry = useCallback(() => {
    invalidateCache(worktreeId, projectId, repoId);
    void loadRef.current(true);
  }, [worktreeId, projectId, repoId]);

  return { data, loading, loadError, waitingOnDaemon, refreshing, reload, refresh, retry };
}

/**
 * Whether this worktree branch already has a PR, and whether the GitHub CLI
 * is available to create one. Re-checks when `refreshKey` changes (after a
 * PR is created). Same stale-response guard as useRepoChanges.
 */
export function useExistingPR(worktreeId: string | undefined, repoId: string | undefined, enabled: boolean, refreshKey: number) {
  const [existingPR, setExistingPR] = useState<gitApi.ExistingPRResponse | null>(null);
  const [ghCliMissing, setGhCliMissing] = useState(false);
  const requestIdRef = useRef(0);

  useEffect(() => {
    const requestId = ++requestIdRef.current;
    setExistingPR(null);
    setGhCliMissing(false);
    if (!worktreeId || !enabled) return;

    const isCurrent = () => requestIdRef.current === requestId;
    gitApi
      .getExistingPR(worktreeId, repoId)
      .then((prInfo) => {
        if (!isCurrent()) return;
        setExistingPR(prInfo);
        if (prInfo.exists) {
          logger.info("Found existing PR for worktree", { url: prInfo.url, state: prInfo.state, repoId });
        }
      })
      .catch((err: unknown) => {
        if (!isCurrent()) return;
        const message = err instanceof Error ? err.message : String(err);
        if (message.includes("GitHub CLI (gh) is not installed")) {
          setGhCliMissing(true);
          logger.info("GitHub CLI not installed - PR features disabled");
        } else {
          logger.debug("Could not check for existing PR", err);
        }
      });
  }, [worktreeId, repoId, enabled, refreshKey]);

  return { existingPR, ghCliMissing };
}
