import { useEffect } from "react";
import { useWorktreeStore } from "../store/worktreeStore";

/**
 * The project's live workspaces, loaded on demand and paged. For consumers that
 * show workspaces without chats — a worktree picker for a new chat, the
 * Workspaces page, the run form. The sidebar and mobile chat list do NOT use
 * this: they group from the chat list, so app start never pages workspaces.
 *
 * Mounting loads the first page (main first); `loadMore` fetches the next while
 * `hasMore`. Concurrent mounts share one request.
 */
export function useWorktreeList(projectId: string | undefined) {
  const worktrees = useWorktreeStore((s) => s.worktrees);
  const loadWorktrees = useWorktreeStore((s) => s.loadWorktrees);
  const loadMoreWorktrees = useWorktreeStore((s) => s.loadMoreWorktrees);
  const hasMore = useWorktreeStore((s) => s.nextPageToken !== "");
  const isLoading = useWorktreeStore((s) => s.isLoading);
  const isLoadingMore = useWorktreeStore((s) => s.isLoadingMore);

  useEffect(() => {
    if (projectId) void loadWorktrees(projectId);
  }, [projectId, loadWorktrees]);

  return {
    worktrees,
    hasMore,
    isLoading,
    isLoadingMore,
    loadMore: () => loadMoreWorktrees(projectId),
  };
}
