import { WorktreeStatus } from "../gen/reliant/v1/worktree_pb";

/**
 * Where a terminal for the active workspace should start.
 *
 * - `ready`: start the shell at `path`.
 * - `pending`: the workspace has no directory yet. CreateWorktree returns the
 *   row CREATING with an empty path and settles it 30-120s later; wait.
 * - `failed`: the workspace never got a directory and never will.
 */
export type TerminalWorkingDir =
  | { kind: "ready"; path: string }
  | { kind: "pending" }
  | { kind: "failed" };

type WorkspaceLike = { path: string; is_main: boolean; status: WorktreeStatus };
type ProjectLike = { path?: string };

/**
 * Resolve a terminal's working directory from the active workspace.
 *
 * The project path is the right answer ONLY when there is no workspace, or the
 * workspace is the main checkout. A non-main workspace without a path must
 * never fall back to it: that shell would run in the main checkout under the
 * workspace's tab, and a `git commit` there lands on the wrong branch. The old
 * `worktree.path || project.path` chain did exactly that for every workspace
 * opened while it was still being created.
 */
export function resolveTerminalWorkingDir(
  workspace: WorkspaceLike | null | undefined,
  project: ProjectLike | null | undefined,
): TerminalWorkingDir {
  if (workspace?.path) {
    return { kind: "ready", path: workspace.path };
  }
  if (workspace && !workspace.is_main) {
    return workspace.status === WorktreeStatus.FAILED ? { kind: "failed" } : { kind: "pending" };
  }
  if (project?.path) {
    return { kind: "ready", path: project.path };
  }
  return { kind: "pending" };
}
