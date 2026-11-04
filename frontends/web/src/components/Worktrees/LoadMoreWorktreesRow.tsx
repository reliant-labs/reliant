import { useWorktreeList } from "../../hooks/worktree-queries";

/**
 * "Load more" for a workspace picker. The picker's list holds one page of the
 * project's workspaces; this fetches the next, so a project with hundreds of
 * agent workspaces stays fully reachable without loading them all up front.
 * Renders nothing on the last page.
 */
export function LoadMoreWorktreesRow({ projectId }: { projectId: string | undefined }) {
  const { hasMore, isLoadingMore, loadMore } = useWorktreeList(projectId);
  if (!hasMore) return null;
  return (
    <button
      type="button"
      data-testid="load-more-worktrees"
      disabled={isLoadingMore}
      onClick={loadMore}
      className="w-full border-b px-3 py-2 text-center text-xs text-muted-foreground transition-colors hover:bg-muted disabled:opacity-60"
    >
      {isLoadingMore ? "Loading…" : "Load more workspaces"}
    </button>
  );
}
