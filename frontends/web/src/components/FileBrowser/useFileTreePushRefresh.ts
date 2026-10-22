/**
 * Refresh the file tree when the server says files changed — but only when the
 * change is about the tree on screen and someone can see it.
 *
 * The tree has no poll. It refreshes on two pushed signals:
 *
 *   - `worktree_changes`, emitted after every file-mutating agent tool call,
 *     scoped to the worktree the tool ran in;
 *   - `file_tree`, emitted when the daemon's filesystem watcher sees a
 *     project's checkout change (edits made outside Reliant), scoped to the
 *     project.
 *
 * Each refresh costs one GetFileTree for the root plus one per expanded
 * directory. Before this hook the sidebar refreshed on EVERY such event for
 * every worktree of every project — so an agent editing files in one workspace
 * refreshed the tree of whichever other workspace was open, once per tool
 * call, even with the Files tab hidden behind another tab. Measured on a dev
 * stack running a few agents at once: 16–49 GetFileTree a minute.
 *
 * So an event refreshes the tree only when it names the tree's worktree or
 * project and the tree is visible. An in-scope event that arrives while the
 * tree is hidden is remembered, and the tree refreshes once when it is shown
 * again — it is never left stale, it just stops paying for updates no one sees.
 * Bursts collapse: an event that arrives while a refresh is in flight queues a
 * single follow-up rather than one per event.
 */

import { useCallback, useEffect, useRef } from "react";
import { subscribeToRefetch, type RefetchEvent } from "../../store/refetchStore";

export interface FileTreeScope {
  projectId?: string | null;
  worktreeId?: string | null;
}

/** Whether a refetch event is about the tree shown for `scope`. */
export function fileTreeEventInScope(event: RefetchEvent, scope: FileTreeScope): boolean {
  // An unscoped event could be about anything, including this tree.
  if (!event.entityId) return true;
  return event.entityId === scope.worktreeId || event.entityId === scope.projectId;
}

function documentHidden(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

export function useFileTreePushRefresh({
  projectId,
  worktreeId,
  visible,
  refresh,
}: FileTreeScope & {
  /** The tree is on screen (its tab is the active one). */
  visible: boolean;
  refresh: () => Promise<void> | void;
}): void {
  const scopeRef = useRef<FileTreeScope>({ projectId, worktreeId });
  scopeRef.current = { projectId, worktreeId };
  const visibleRef = useRef(visible);
  visibleRef.current = visible;
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  const missedWhileHidden = useRef(false);
  const inFlight = useRef(false);
  const queued = useRef(false);

  const run = useCallback(() => {
    if (inFlight.current) {
      queued.current = true;
      return;
    }
    inFlight.current = true;
    void Promise.resolve()
      .then(() => refreshRef.current())
      .catch(() => {
        // The tree reports its own load errors; a failed refresh just leaves
        // the previous listing up.
      })
      .finally(() => {
        inFlight.current = false;
        if (queued.current) {
          queued.current = false;
          run();
        }
      });
  }, []);

  useEffect(() => {
    const onEvent = (event: RefetchEvent) => {
      if (!fileTreeEventInScope(event, scopeRef.current)) return;
      if (!visibleRef.current || documentHidden()) {
        missedWhileHidden.current = true;
        return;
      }
      run();
    };
    const unsubFileTree = subscribeToRefetch("file_tree", onEvent);
    const unsubWorktree = subscribeToRefetch("worktree_changes", onEvent);
    return () => {
      unsubFileTree();
      unsubWorktree();
    };
  }, [run]);

  // Shown again (tab re-selected, or the window un-hidden) after missing a
  // change: catch up with one refresh.
  useEffect(() => {
    const catchUp = () => {
      if (!missedWhileHidden.current || !visibleRef.current || documentHidden()) return;
      missedWhileHidden.current = false;
      run();
    };
    catchUp();
    document.addEventListener("visibilitychange", catchUp);
    return () => document.removeEventListener("visibilitychange", catchUp);
  }, [visible, run]);

  // A different worktree or project is a fresh load (FileTree reloads itself
  // on worktree change), so whatever the old one missed no longer matters.
  useEffect(() => {
    missedWhileHidden.current = false;
  }, [projectId, worktreeId]);
}
