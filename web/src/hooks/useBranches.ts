import { useState, useEffect, useCallback, useRef } from "react";
import { api } from "../api/client";

export interface GitBranch {
  name: string;
  is_current: boolean;
  is_remote: boolean;
  upstream?: string;
  last_commit_age?: number; // seconds since last commit
  is_detached?: boolean; // true when HEAD is in detached state
  commit_sha?: string; // full commit SHA (only set when is_detached is true)
}

export interface UseBranchesReturn {
  branches: GitBranch[];
  isLoading: boolean;
  error: string | null;
  refetch: () => Promise<void>;
}

/**
 * Loads git branches for a project.
 *
 * `repoId` selects which nested repo to read. It is required in multi-repo
 * projects: the backend resolves the repo path from it and returns
 * InvalidArgument ("repo_id required in multi-repo projects") when a project
 * has 2+ repos and no repo is named. Single-repo projects may omit it.
 */
export function useBranches(
  projectId: string | undefined,
  repoId?: string,
): UseBranchesReturn {
  const [branches, setBranches] = useState<GitBranch[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const cancelledRef = useRef(false);

  const fetchBranches = useCallback(async () => {
    if (!projectId) {
      setBranches([]);
      return;
    }

    setIsLoading(true);
    setError(null);

    try {
      const response = await api.git.getBranches(projectId, repoId);

      if (cancelledRef.current) return;

      // Empty branches array is valid for non-git projects
      if (!response.branches) {
        setBranches([]);
      } else if (response.branches.length === 0) {
        setBranches([]);
        // Don't set error for empty branches - it's a valid state
      } else {
        setBranches(response.branches);
      }
    } catch (err) {
      if (cancelledRef.current) return;
      const errorMessage =
        err instanceof Error ? err.message : "Failed to fetch branches";
      setError(errorMessage);
      setBranches([]);
    } finally {
      if (!cancelledRef.current) {
        setIsLoading(false);
      }
    }
  }, [projectId, repoId]);

  useEffect(() => {
    cancelledRef.current = false;
    fetchBranches();
    return () => {
      cancelledRef.current = true;
    };
  }, [fetchBranches]);

  return {
    branches,
    isLoading,
    error,
    refetch: fetchBranches,
  };
}
