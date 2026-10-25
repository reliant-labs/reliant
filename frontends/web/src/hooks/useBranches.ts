import { useCallback } from "react";

import type { GetProjectGitBranchesResponse } from "../gen/reliant/v1/project_pb";
import { useGetProjectGitBranches } from "./project-service-hooks_gen";

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

const EMPTY_BRANCHES: GitBranch[] = [];

function branchesOf(response: GetProjectGitBranchesResponse): GitBranch[] {
  return response.branches.map((branch) => ({
    name: branch.name,
    is_current: branch.isCurrent,
    is_remote: branch.isRemote,
    upstream: branch.upstream,
    last_commit_age: Number(branch.lastCommitAge),
  }));
}

/**
 * Loads git branches for a project (GetProjectGitBranches, on the generated
 * hook and key — so a git init, which invalidates every project read,
 * refetches it).
 *
 * `repoId` selects which nested repo to read. It is required in multi-repo
 * projects: the backend resolves the repo path from it and returns
 * InvalidArgument ("repo_id required in multi-repo projects") when a project
 * has 2+ repos and no repo is named. Single-repo projects may omit it. An
 * empty list is a valid answer for a project that is not a git repo.
 */
export function useBranches(
  projectId: string | undefined,
  repoId?: string,
): UseBranchesReturn {
  const query = useGetProjectGitBranches(
    { projectId: projectId ?? "", repoId: repoId ?? "" },
    { enabled: !!projectId, staleTime: 10_000, retry: false, select: branchesOf },
  );
  const { refetch } = query;
  const refetchBranches = useCallback(async () => {
    await refetch();
  }, [refetch]);

  if (!projectId) {
    return { branches: EMPTY_BRANCHES, isLoading: false, error: null, refetch: refetchBranches };
  }
  return {
    branches: query.error ? EMPTY_BRANCHES : (query.data ?? EMPTY_BRANCHES),
    isLoading: query.isFetching,
    error: query.error ? query.error.message || "Failed to fetch branches" : null,
    refetch: refetchBranches,
  };
}
