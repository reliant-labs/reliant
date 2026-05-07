import { worktreeGrpc } from "./worktree-grpc";
import { triggerImmediateGitStatusRefresh } from "../store/gitStatusStore";
import { useWorktreeStore } from "../store/worktreeStore";

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

// Helper to get projectId from worktreeId for refresh triggers
function getProjectIdFromWorktree(worktreeId: string): string | undefined {
  const worktree = useWorktreeStore.getState().worktrees.find(w => w.id === worktreeId);
  return worktree?.project_id;
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
  const result = await worktreeGrpc.stageFiles(worktreeId, files, repoId);
  // Trigger immediate refresh after staging
  const projectId = getProjectIdFromWorktree(worktreeId);
  if (projectId) triggerImmediateGitStatusRefresh(worktreeId, projectId);
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
  const result = await worktreeGrpc.unstageFiles(worktreeId, files, repoId);
  // Trigger immediate refresh after unstaging
  const projectId = getProjectIdFromWorktree(worktreeId);
  if (projectId) triggerImmediateGitStatusRefresh(worktreeId, projectId);
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
  const result = await worktreeGrpc.commit(worktreeId, message, repoId);
  // Trigger immediate refresh after commit
  const projectId = getProjectIdFromWorktree(worktreeId);
  if (projectId) triggerImmediateGitStatusRefresh(worktreeId, projectId);
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
  const result = await worktreeGrpc.push(worktreeId, repoId);
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
  const result = await worktreeGrpc.pull(worktreeId, repoId);
  // Trigger immediate refresh after pull
  const projectId = getProjectIdFromWorktree(worktreeId);
  if (projectId) triggerImmediateGitStatusRefresh(worktreeId, projectId);
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
  const result = await worktreeGrpc.getPR(worktreeId, repoId);
  return {
    exists: result.exists,
    url: result.url,
    number: result.number,
    title: result.title,
    state: result.state,
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
  const result = await worktreeGrpc.createPR(worktreeId, title, body, repoId);
  return {
    message: result.message,
    pr_url: result.pr_url,
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
  const result = await worktreeGrpc.revertFiles(worktreeId, files, repoId);
  // Trigger immediate refresh after revert
  const projectId = getProjectIdFromWorktree(worktreeId);
  if (projectId) triggerImmediateGitStatusRefresh(worktreeId, projectId);
  return {
    message: result.message,
    files: result.files,
  };
}
