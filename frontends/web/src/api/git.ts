import { WorktreeService } from "../gen/reliant/v1/worktree_pb";
import { invalidateWorktreeQueries, getCachedWorktree } from "../hooks/worktree-queries";
import { connectClient } from "../lib/connect";
import { queryClient } from "../lib/query-client";
import { triggerImmediateGitStatusRefresh } from "../store/gitStatusStore";
import { useProjectStore } from "../store/projectStore";

const client = connectClient(WorktreeService);

export interface StageFilesRequest {
  files: string[]; // Array of file paths to stage, or ["."] to stage all
}

export interface UnstageFilesRequest {
  files: string[]; // Array of file paths to unstage, or ["."] to unstage all
}

export interface CommitRequest {
  message: string;
}

export interface CreatePRRequest {
  title: string;
  body?: string;
}

export interface GitOperationResponse {
  message: string;
  files?: string[];
  output?: string;
  pr_url?: string;
}

// Helper to get projectId from worktreeId for refresh triggers. The cache holds
// only the workspaces something asked for, so a miss falls back to the open
// project: git operations run in the workspace the user is looking at.
function getProjectIdFromWorktree(worktreeId: string): string | undefined {
  return getCachedWorktree(worktreeId)?.project_id ?? useProjectStore.getState().currentProject?.id;
}

/**
 * After a git write: refresh every cached read of the workspace (its changes,
 * status, commits, PR) and tell the legacy git-status subscribers.
 */
function afterWrite(worktreeId: string, notify = true): void {
  void invalidateWorktreeQueries(queryClient, worktreeId);
  if (!notify) return;
  const projectId = getProjectIdFromWorktree(worktreeId);
  if (projectId) triggerImmediateGitStatusRefresh(worktreeId, projectId);
}

export interface ExistingPRResponse {
  exists: boolean;
  url?: string;
  number?: number;
  title?: string;
  state?: string; // "OPEN" | "CLOSED" | "MERGED"
}

/**
 * Stage files in a worktree.
 * repoId scopes to a nested repo (single-repo callers omit). File paths
 * must be relative to that repo.
 */
export async function stageFiles(
  worktreeId: string,
  files: string[],
  repoId?: string
): Promise<GitOperationResponse> {
  const result = await client.stageFiles({ worktreeId, files, repoId: repoId ?? "" });
  afterWrite(worktreeId);
  return {
    message: result.message,
    files: result.files,
  };
}

/**
 * Unstage files in a worktree.
 * repoId scopes to a nested repo (single-repo callers omit).
 */
export async function unstageFiles(
  worktreeId: string,
  files: string[],
  repoId?: string
): Promise<GitOperationResponse> {
  const result = await client.unstageFiles({ worktreeId, files, repoId: repoId ?? "" });
  afterWrite(worktreeId);
  return {
    message: result.message,
    files: result.files,
  };
}

/**
 * Commit staged changes in a worktree.
 * repoId scopes to a nested repo (single-repo callers omit).
 */
export async function commitChanges(
  worktreeId: string,
  message: string,
  repoId?: string
): Promise<GitOperationResponse> {
  const result = await client.commitWorktree({ worktreeId, message, repoId: repoId ?? "" });
  afterWrite(worktreeId);
  return {
    message: result.message,
    output: result.output,
  };
}

/**
 * Push commits to remote.
 * repoId scopes to a nested repo (each repo has its own remote).
 */
export async function pushChanges(
  worktreeId: string,
  repoId?: string
): Promise<GitOperationResponse> {
  const result = await client.pushWorktree({ worktreeId, repoId: repoId ?? "" });
  afterWrite(worktreeId, false);
  return {
    message: result.message,
    output: result.output,
  };
}

/**
 * Pull changes from remote.
 * repoId scopes to a nested repo (each repo has its own remote).
 */
export async function pullChanges(
  worktreeId: string,
  repoId?: string
): Promise<GitOperationResponse> {
  const result = await client.pullWorktree({ worktreeId, repoId: repoId ?? "" });
  afterWrite(worktreeId);
  return {
    message: result.message,
    output: result.output,
  };
}

/**
 * Check if a PR already exists for this worktree's branch.
 * repoId scopes to a nested repo (PRs are per-repo).
 */
export async function getExistingPR(
  worktreeId: string,
  repoId?: string
): Promise<ExistingPRResponse> {
  const result = await client.getWorktreePR({ worktreeId, repoId: repoId ?? "" });
  return {
    exists: result.exists,
    url: result.url || undefined,
    number: result.number || undefined,
    title: result.title || undefined,
    state: result.state || undefined,
  };
}

/**
 * Create a pull request.
 * repoId scopes to a nested repo (PRs are per-repo).
 */
export async function createPullRequest(
  worktreeId: string,
  title: string,
  body?: string,
  repoId?: string
): Promise<GitOperationResponse> {
  const result = await client.createWorktreePR({ worktreeId, title, body, repoId: repoId ?? "" });
  afterWrite(worktreeId, false);
  return {
    message: result.message,
    pr_url: result.prUrl,
    output: result.output,
  };
}

/**
 * Revert/discard file changes in a worktree
 * For staged files: unstages and discards changes
 * For modified files: discards working tree changes
 * For untracked files: deletes the file
 * repoId scopes to a nested repo (single-repo callers omit).
 */
export async function revertFiles(
  worktreeId: string,
  files: string[],
  repoId?: string
): Promise<GitOperationResponse> {
  const result = await client.revertFiles({ worktreeId, files, repoId: repoId ?? "" });
  afterWrite(worktreeId);
  return {
    message: result.message,
    files: result.files,
  };
}
