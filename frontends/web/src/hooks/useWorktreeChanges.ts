/**
 * useWorktreeChanges - Hook for fetching worktree file changes with diff stats
 * 
 * Provides file count, additions, and deletions for a worktree.
 * Instead of polling, subscribes to "worktree_changes" refetch events
 * from the stream. The backend emits these after file-mutating tool calls.
 * 
 * Refetch events are debounced (500ms) so rapid-fire tool completions
 * collapse into a single GetWorktreeChanges call.
 */

import { useState, useEffect, useRef, useCallback } from "react";
import { worktreeGrpc } from "../api/worktree-grpc";
import { FileChangeStatus } from "../gen/reliant/v1/common_pb";
import { logger } from "../lib/logger";
import { matchesRefetchScope, subscribeToRefetch } from "../store/refetchStore";

export interface WorktreeChangeStats {
  totalFiles: number;
  additions: number;
  deletions: number;
  branch: string;
  ahead: number;
  behind: number;
  isLoading: boolean;
  error: string | null;
}

const INITIAL_STATS: WorktreeChangeStats = {
  totalFiles: 0,
  additions: 0,
  deletions: 0,
  branch: "",
  ahead: 0,
  behind: 0,
  isLoading: false,
  error: null,
};

/** Debounce delay for refetch-triggered calls (ms) */
const REFETCH_DEBOUNCE_MS = 500;

/**
 * Parse a unified diff to count additions and deletions
 * For new files (raw content, not diff format), count all lines as additions
 */
function countDiffStats(diff: string, isNewFile: boolean): { additions: number; deletions: number } {
  if (!diff) return { additions: 0, deletions: 0 };
  
  const lines = diff.split("\n");
  
  // For new/untracked files, the "diff" is actually raw file content
  // Count all non-empty lines as additions
  if (isNewFile) {
    // Don't count the trailing empty line from split
    const nonEmptyLines = lines.filter((line, idx) => {
      // Keep all lines except trailing empty line from split
      return idx < lines.length - 1 || line !== "";
    });
    return { additions: nonEmptyLines.length, deletions: 0 };
  }
  
  // For modified files, parse the unified diff format
  let additions = 0;
  let deletions = 0;
  
  for (const line of lines) {
    // Skip diff headers
    if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("@@")) {
      continue;
    }
    // Skip other metadata lines
    if (line.startsWith("diff --git") || line.startsWith("index ") || 
        line.startsWith("new file") || line.startsWith("deleted file")) {
      continue;
    }
    // Count additions (lines starting with +)
    if (line.startsWith("+")) {
      additions++;
    }
    // Count deletions (lines starting with -)
    else if (line.startsWith("-")) {
      deletions++;
    }
  }
  
  return { additions, deletions };
}

/**
 * Hook to fetch and track worktree file changes with diff statistics.
 * Subscribes to refetch events instead of polling.
 */
export function useWorktreeChanges(worktreeId: string | null | undefined): WorktreeChangeStats {
  const [stats, setStats] = useState<WorktreeChangeStats>(INITIAL_STATS);
  const isMounted = useRef(true);
  const worktreeIdRef = useRef(worktreeId);
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  
  // Keep ref in sync
  worktreeIdRef.current = worktreeId;

  const fetchChanges = useCallback(async (isInitial: boolean) => {
    const currentWorktreeId = worktreeIdRef.current;
    if (!currentWorktreeId) return;

    // Only show loading on initial fetch
    if (isInitial) {
      setStats(prev => ({ ...prev, isLoading: true, error: null }));
    }

    try {
      const changes = await worktreeGrpc.getChanges(currentWorktreeId);
      
      if (!isMounted.current) return;
      // Check if worktreeId changed while we were fetching
      if (worktreeIdRef.current !== currentWorktreeId) return;

      // Calculate total additions and deletions from all file diffs
      let totalAdditions = 0;
      let totalDeletions = 0;
      
      for (const file of changes.files) {
        // is_new is true for newly staged files, "untracked" status for untracked files
        const isNewFile =
          file.is_new || file.status === FileChangeStatus.UNTRACKED;
        const { additions, deletions } = countDiffStats(file.diff, isNewFile);
        totalAdditions += additions;
        totalDeletions += deletions;
      }

      setStats({
        totalFiles: changes.total_files,
        additions: totalAdditions,
        deletions: totalDeletions,
        branch: changes.branch,
        ahead: changes.ahead,
        behind: changes.behind,
        isLoading: false,
        error: null,
      });
    } catch (err) {
      if (!isMounted.current) return;
      if (worktreeIdRef.current !== currentWorktreeId) return;
      
      const errorMessage = err instanceof Error ? err.message : "Failed to fetch changes";
      logger.error("[useWorktreeChanges] Failed to fetch changes:", err);
      
      // Only set error on initial fetch, silently fail on refetches
      if (isInitial) {
        setStats(prev => ({
          ...prev,
          isLoading: false,
          error: errorMessage,
        }));
      }
    }
  }, []);

  /**
   * Debounced fetch — collapses rapid-fire refetch events into a single call.
   * Each new call resets the timer, so only the last event in a burst triggers a fetch.
   */
  const debouncedFetch = useCallback(() => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
    }
    debounceTimerRef.current = setTimeout(() => {
      debounceTimerRef.current = null;
      fetchChanges(false);
    }, REFETCH_DEBOUNCE_MS);
  }, [fetchChanges]);

  useEffect(() => {
    isMounted.current = true;
    
    // Reset stats when worktreeId changes
    if (!worktreeId) {
      setStats(INITIAL_STATS);
      return;
    }

    // Initial fetch (not debounced)
    fetchChanges(true);

    // Subscribe to refetch events (fast path: backend signals after tool calls)
    // Debounced so rapid-fire events collapse into one call
    const unsubscribe = subscribeToRefetch("worktree_changes", (event) => {
      if (!matchesRefetchScope(event, { worktreeId })) {
        return;
      }
      debouncedFetch();
    });

    // Slow fallback poll to catch external filesystem changes
    // (user editing in another editor, git pull, etc.)
    const fallbackInterval = setInterval(() => {
      fetchChanges(false);
    }, 30_000);

    return () => {
      isMounted.current = false;
      unsubscribe();
      clearInterval(fallbackInterval);
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current);
        debounceTimerRef.current = null;
      }
    };
  }, [worktreeId, fetchChanges, debouncedFetch]);

  return stats;
}
