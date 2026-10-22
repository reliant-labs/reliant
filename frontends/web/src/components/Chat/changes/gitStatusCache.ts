import { worktreeGrpc } from "../../../api/worktree-grpc";
import { projectGrpc, type FileChange as GrpcFileChange } from "../../../api/project-grpc";
import { useProjectStore } from "../../../store/projectStore";
import { logger } from "../../../lib/logger";
import type { FileChange, RecentChangesData } from "./types";

// Module-level cache for git status data so reopening the Changes tab (or
// switching back to a chat) renders instantly instead of flashing a skeleton.
// Keyed by (worktreeId|projectId, repoId) so multi-repo panels don't stomp on
// each other's entries.
const gitStatusCache = new Map<string, { data: RecentChangesData; timestamp: number }>();
const CACHE_TTL = 5000; // 5 second cache TTL

function getCacheKey(worktreeId: string | undefined, projectId: string, repoId?: string): string {
  const base = worktreeId ? `worktree:${worktreeId}` : `project:${projectId}`;
  return repoId ? `${base}|repo:${repoId}` : base;
}

export function getCachedData(
  worktreeId: string | undefined,
  projectId: string,
  repoId?: string,
): RecentChangesData | null {
  const key = getCacheKey(worktreeId, projectId, repoId);
  const cached = gitStatusCache.get(key);
  if (!cached) return null;
  if (Date.now() - cached.timestamp > CACHE_TTL) {
    gitStatusCache.delete(key);
    return null;
  }
  return cached.data;
}

export function setCachedData(
  worktreeId: string | undefined,
  projectId: string,
  data: RecentChangesData,
  repoId?: string,
): void {
  gitStatusCache.set(getCacheKey(worktreeId, projectId, repoId), { data, timestamp: Date.now() });
}

export function invalidateCache(worktreeId: string | undefined, projectId: string, repoId?: string): void {
  gitStatusCache.delete(getCacheKey(worktreeId, projectId, repoId));
}

/**
 * Fetch changes for a worktree (optionally one nested repo) or, without a
 * worktree, for the project's main checkout. Project changes carry no
 * ahead/behind, so those are reported as 0 and the project's default branch
 * stands in for PR targeting.
 */
export async function fetchChanges(
  worktreeId: string | undefined,
  projectId: string,
  repoId: string | undefined,
  projectDefaultBranch: string,
): Promise<RecentChangesData> {
  if (worktreeId) {
    const grpcData = await worktreeGrpc.getChanges(worktreeId, repoId);
    return {
      branch: grpcData.branch,
      files: grpcData.files.map(
        (f): FileChange => ({ path: f.path, status: f.status, diff: f.diff, is_new: f.is_new }),
      ),
      total_files: grpcData.total_files,
      ahead: grpcData.ahead,
      behind: grpcData.behind,
      default_branch: grpcData.default_branch,
    };
  }

  const grpcData = await projectGrpc.getChanges(projectId);
  return {
    branch: grpcData.branch,
    files: grpcData.files.map(
      (f: GrpcFileChange): FileChange => ({
        path: f.path,
        status: f.status,
        diff: f.diff,
        content: f.content,
        original_content: f.original_content,
        is_new: f.is_new,
      }),
    ),
    total_files: grpcData.total_files,
    ahead: 0,
    behind: 0,
    default_branch: projectDefaultBranch,
  };
}

// Preload function that can be called from outside the component.
// (Single-repo preload — multi-repo projects load on demand.)
export async function preloadGitStatus(
  worktreeId: string | undefined,
  projectId: string,
  isGitRepo: boolean,
): Promise<void> {
  if (!isGitRepo || (!worktreeId && !projectId)) return;
  if (getCachedData(worktreeId, projectId)) return;

  try {
    const defaultBranch = useProjectStore.getState().currentProject?.default_branch ?? "";
    const changesData = await fetchChanges(worktreeId, projectId, undefined, defaultBranch);
    setCachedData(worktreeId, projectId, changesData);
    logger.debug("[RecentChanges] Preloaded git status", { worktreeId, projectId, fileCount: changesData.total_files });
  } catch (err) {
    // Silently fail - preloading shouldn't break the app
    logger.debug("[RecentChanges] Failed to preload git status", err);
  }
}
