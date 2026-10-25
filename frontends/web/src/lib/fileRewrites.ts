// Copyright (c) 2025 Reliant Labs

/**
 * "These files were rewritten on disk by something other than their editor."
 *
 * An open editor holds its buffer in component state (FileViewerTab's
 * `content` / `originalContent`), not in a cache a write could invalidate,
 * and that is deliberate: a refetch must never overwrite an unsaved edit. So a
 * bulk write that changes files under an open editor (ReplaceInFiles) says
 * which files it rewrote, and each editor decides for itself: a clean buffer
 * reloads, a dirty one is left alone.
 *
 * Kept free of imports so the api module and the editor can both use it
 * without either depending on the other.
 */

export interface FileRewrite {
  /** The workspace the write ran in; undefined is the project's main checkout. */
  worktreeId?: string;
  /** The rewritten files, as the server named them. */
  paths: string[];
}

type Listener = (rewrite: FileRewrite) => void;

const listeners = new Set<Listener>();

/** Tell every open editor that these files changed on disk. */
export function notifyFilesRewritten(rewrite: FileRewrite): void {
  if (rewrite.paths.length === 0) return;
  for (const listener of listeners) {
    listener(rewrite);
  }
}

/** Listen for rewrites; returns the unsubscribe. */
export function subscribeFileRewrites(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * Whether a rewrite names the file an editor has open. The server names files
 * by absolute path, while an editor may hold a workspace-relative one, so a
 * relative path matches the absolute path ending in it, but only within the
 * same workspace: `src/a.ts` in two worktrees is two files.
 */
export function rewriteTouches(rewrite: FileRewrite, path: string, worktreeId: string | undefined): boolean {
  const sameWorkspace = (rewrite.worktreeId || undefined) === (worktreeId || undefined);
  return rewrite.paths.some((rewritten) => {
    if (rewritten === path) return true;
    if (!sameWorkspace) return false;
    const relative = (p: string) => !p.startsWith("/");
    if (relative(path) && !relative(rewritten)) return rewritten.endsWith(`/${path}`);
    if (relative(rewritten) && !relative(path)) return path.endsWith(`/${rewritten}`);
    return false;
  });
}
