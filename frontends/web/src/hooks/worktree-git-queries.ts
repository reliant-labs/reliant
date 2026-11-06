// Copyright (c) 2025 Reliant Labs

/**
 * A workspace's git state — changes, status, commits, per-repo summaries, its
 * PR — on WorktreeService's generated reads, plus the git writes (stage,
 * commit, push, ...) that change it.
 *
 * Every read is computed by the daemon against the checkout on disk, so each
 * is a machine round trip; the `worktree_changes` stream event (fired after a
 * file-mutating tool call) invalidates the reads that name its workspace (see
 * hooks/worktree-queries.ts), and every write here invalidates the same scope.
 * The cache holds the proto responses under `worktreeServiceKeys`; the hooks
 * map them to the plain types below with `select`.
 */

import { useEffect, useState } from "react";
import { useQuery, type QueryClient } from "@tanstack/react-query";
import { DaemonStatus } from "../gen/reliant/v1/daemon_registry_pb";
import { daemonListQuery } from "./daemon-registry-queries";
import { useWorktreeById } from "./worktree-queries";

import type { FileChangeStatus } from "../gen/reliant/v1/common_pb";
import type {
  GetWorktreeChangesResponse,
  GetWorktreeCommitsResponse,
  GetWorktreeGitStatusResponse,
  ListWorktreeRepoStatusesResponse,
} from "../gen/reliant/v1/worktree_pb";
import { queryClient as appQueryClient } from "../lib/query-client";
import {
  useGetWorktreeCommits,
  useGetWorktreeGitStatus,
  useListWorktreeRepoStatuses,
  worktreeServiceQueries,
} from "./worktree-service-hooks_gen";

// ── Frontend types ──────────────────────────────────────────────────────────

export interface GitCommit {
  hash: string;
  short_hash: string;
  author: string;
  email: string;
  date: string;
  message: string;
}

export interface WorktreeFileChange {
  path: string;
  status: FileChangeStatus;
  is_new: boolean;
  diff: string;
}

export interface WorktreeChanges {
  files: WorktreeFileChange[];
  total_files: number;
  branch: string;
  ahead: number;
  behind: number;
  default_branch: string; // Repository's default branch (for PR targeting)
}

export interface WorktreeGitStatus {
  is_clean: boolean;
  current_branch: string;
  ahead: number;
  behind: number;
  staged_files: string[];
  modified_files: string[];
  untracked_files: string[];
}

export interface WorktreeCommits {
  commits: GitCommit[];
  total: number;
  branch: string;
  base_branch: string;
  comparison_mode: boolean;
  comparison_ref: string;
  current_branch: string;
}

// Per-repo summary row used by the right-sidebar grouped view.
// Mirrors reliant.v1.WorktreeRepoStatus.
export interface WorktreeRepoStatus {
  repo_id: string;
  repo_name: string;
  repo_relative_path: string;
  current_branch: string;
  has_changes: boolean;
  ahead: number;
  behind: number;
  changed_files: number;
  error: string;
}

// ── Proto mapping ───────────────────────────────────────────────────────────

export function changesFromProto(response: GetWorktreeChangesResponse): WorktreeChanges {
  return {
    files: response.files.map((f) => ({ path: f.path, status: f.status, is_new: f.isNew, diff: f.diff })),
    total_files: response.totalFiles,
    branch: response.branch,
    ahead: response.ahead,
    behind: response.behind,
    default_branch: response.defaultBranch,
  };
}

export function gitStatusFromProto(response: GetWorktreeGitStatusResponse): WorktreeGitStatus {
  return {
    is_clean: response.clean,
    current_branch: response.branch,
    ahead: response.ahead,
    behind: response.behind,
    staged_files: response.stagedFiles,
    modified_files: response.modifiedFiles,
    untracked_files: response.untrackedFiles,
  };
}

export function commitsFromProto(response: GetWorktreeCommitsResponse): WorktreeCommits {
  return {
    commits: response.commits.map((c) => ({
      hash: c.hash,
      short_hash: c.shortHash,
      author: c.author,
      email: c.email,
      date: c.date,
      message: c.message,
    })),
    total: response.total,
    branch: response.branch,
    base_branch: response.baseBranch,
    comparison_mode: response.comparisonMode,
    comparison_ref: response.comparisonRef,
    current_branch: response.currentBranch,
  };
}

function repoStatusesFromProto(response: ListWorktreeRepoStatusesResponse): WorktreeRepoStatus[] {
  return response.statuses.map((s) => ({
    repo_id: s.repoId,
    repo_name: s.repoName,
    repo_relative_path: s.repoRelativePath,
    current_branch: s.currentBranch,
    has_changes: s.hasChanges,
    ahead: s.ahead,
    behind: s.behind,
    changed_files: s.changedFiles,
    error: s.error,
  }));
}

// Branch is deliberately not a wire field. Extend the generated key, keeping
// its request object intact for invalidateWorktreeQueries. The generated query
// factory is also what useGetWorktreePR uses; its hook cannot extend the key.

export type PullRequestLookup = {
  status: "loading" | "found" | "absent" | "error";
  existing: { exists: boolean; url: string; number: number; state: string; title: string } | null;
  error: string | null;
  refreshing: boolean;
  retry: () => void;
};

export function useWorktreePR({
  worktreeId, repoId, branch, enabled = true, visible = true, machineActive = false,
}: {
  worktreeId?: string;
  repoId?: string;
  branch?: string;
  enabled?: boolean;
  visible?: boolean;
  /** Positive evidence only: automatic metadata reads must not wake a machine. */
  machineActive?: boolean;
}): PullRequestLookup {
  const request = changesRequest(worktreeId ?? "", repoId);
  const options = worktreeServiceQueries.getWorktreePR(request);
  const [documentVisible, setDocumentVisible] = useState(() => document.visibilityState !== "hidden");
  useEffect(() => {
    const update = () => setDocumentVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);

  const active = enabled && visible && documentVisible && machineActive && !!worktreeId && branch !== undefined;
  const query = useQuery({
    ...options,
    queryKey: [...options.queryKey, "observed-branch", branch ?? ""],
    // Do not resurrect a previous visit's branch association. In-flight results
    // are scoped to their own key, and concurrent readers still deduplicate.
    gcTime: 0,
    enabled: active,
    retry: false,
    staleTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: "always",
    refetchOnReconnect: "always",
    refetchIntervalInBackground: false,
    // One minute normally, exponential error backoff capped at five minutes.
    refetchInterval: (q) => active
      ? q.state.status === "error" ? Math.min(300_000, 60_000 * 2 ** Math.min(q.state.errorUpdateCount, 3)) : 60_000
      : false,
  });
  const data = branch !== undefined ? query.data : undefined;
  const error = branch !== undefined && query.error ? query.error.message
    : !machineActive && branch !== undefined ? "Connect or wake the workspace machine to check its pull request." : null;
  const existing = data?.exists && data.url ? {
    exists: true, url: data.url, number: data.number ?? 0,
    state: data.state ?? "", title: data.title ?? "",
  } : null;
  return {
    status: existing ? "found" : error ? "error" : data && !data.exists ? "absent" : "loading",
    existing,
    error,
    refreshing: query.isFetching,
    retry: () => { if (worktreeId && branch !== undefined) void query.refetch(); },
  };
}

/** Subscribe to registry push updates without waking or polling the machine. */
export function useWorktreeMachineActive(worktreeId?: string) {
  const worktree = useWorktreeById(worktreeId);
  const { data: daemons } = useQuery(daemonListQuery({ enabled: !!worktreeId }));
  return !!worktree?.daemon_id && !!daemons?.some((d) => d.daemonId === worktree.daemon_id && d.status === DaemonStatus.ACTIVE);
}

// ── Reads ───────────────────────────────────────────────────────────────────

// repoId scopes a read to one nested repo. Empty uses the single-repo
// behaviour (the server answers InvalidArgument when the project has 2+).
const changesRequest = (worktreeId: string, repoId?: string) => ({ worktreeId, repoId: repoId ?? "" });


/**
 * A workspace's changed files, always fresh, through the GetWorktreeChanges
 * cache entry: concurrent callers (several mounted panels, a preload, the
 * fallback poll) share one in-flight request instead of each pinning a
 * connection while a slow daemon computes large diffs. `gcTime: 0` keeps no
 * second copy of the diffs once the request settles — the Changes panel holds
 * its own (components/Chat/changes/gitStatusCache.ts).
 */
export async function fetchWorktreeChanges(
  worktreeId: string,
  repoId?: string,
  qc: QueryClient = appQueryClient,
): Promise<WorktreeChanges> {
  const response = await qc.fetchQuery(
    worktreeServiceQueries.getWorktreeChanges(changesRequest(worktreeId, repoId), {
      staleTime: 0,
      gcTime: 0,
      retry: false,
    }),
  );
  return changesFromProto(response);
}

/** One repo's git status, through the entry {@link useWorkspaceGitStatus} reads. */
export async function fetchWorktreeGitStatus(
  worktreeId: string,
  repoId?: string,
  qc: QueryClient = appQueryClient,
): Promise<WorktreeGitStatus> {
  const response = await qc.fetchQuery(
    worktreeServiceQueries.getWorktreeGitStatus(changesRequest(worktreeId, repoId), { retry: false }),
  );
  return gitStatusFromProto(response);
}

/**
 * A workspace's git status. The status is computed by the daemon against the
 * worktree on disk, so this fails whenever no machine is attached — callers
 * render that as "unavailable", never as an error state, and it is not retried.
 */
export function useWorkspaceGitStatus(worktreeId: string | undefined, enabled = true) {
  return useGetWorktreeGitStatus(changesRequest(worktreeId ?? ""), {
    enabled: enabled && !!worktreeId,
    select: gitStatusFromProto,
    staleTime: 30_000,
    retry: false,
    refetchOnWindowFocus: false,
  });
}

/** Commits on the workspace's branch that are not on its base branch. */
export function useWorktreeCommits(worktreeId: string, options?: { limit?: number; repoId?: string }) {
  return useGetWorktreeCommits(
    { worktreeId, limit: options?.limit || 20, repoId: options?.repoId ?? "" },
    { enabled: !!worktreeId, select: commitsFromProto },
  );
}

/**
 * Per-repo git status rows for every nested repo in the workspace's project.
 * Drives the multi-repo Changes panel; single-repo / zero-repo projects
 * collapse to a one- or zero-element list.
 */
export function useWorktreeRepoStatuses(worktreeId: string | null | undefined) {
  return useListWorktreeRepoStatuses(
    { worktreeId: worktreeId ?? "" },
    { enabled: !!worktreeId, select: repoStatusesFromProto, retry: false },
  );
}
