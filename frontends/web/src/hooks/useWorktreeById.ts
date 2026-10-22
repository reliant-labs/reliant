import { useEffect } from "react";
import { useWorktreeStore, type Worktree } from "../store/worktreeStore";

/**
 * One workspace by id, whether or not any list holds it. Reads the store and,
 * when it lacks the row, fetches that row (once, shared with every other caller
 * asking for the same id). For a component handed a worktree id that needs its
 * path or branch — a file viewer, a browser tab — and must not depend on the
 * project's worktree list having been loaded.
 */
export function useWorktreeById(worktreeId: string | undefined): Worktree | undefined {
  const held = useWorktreeStore((s) => (worktreeId ? s.worktrees.find((w) => w.id === worktreeId) : undefined));
  const resolveWorktree = useWorktreeStore((s) => s.resolveWorktree);
  useEffect(() => {
    if (worktreeId && !held) void resolveWorktree(worktreeId);
  }, [worktreeId, held, resolveWorktree]);
  return held;
}
