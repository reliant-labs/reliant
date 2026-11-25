import { useMutation, useQueryClient } from "@tanstack/react-query";
import { refreshAfterArchiveChange } from "./worktree-actions";
import { useRecreateWorktree } from "./worktree-service-hooks_gen";
import { chatKeys } from "./chat-queries";
import * as chatActions from "./chat-actions";

/**
 * Rebuild a chat's workspace directory from its branch when it has gone
 * missing from disk. The server asks the machine that owns it; a workspace
 * that is still there is left untouched.
 */
export function useRecreateWorkspace() {
  const queryClient = useQueryClient();
  // Re-read this workspace and the lists, not every WorktreeService read (the
  // generated refresh): other workspaces' diffs and git status are each a
  // round trip to a machine. Awaited, so the toast follows the refreshed row.
  return useRecreateWorktree({
    invalidate: false,
    onSettled: (response, _error, { worktreeId }) =>
      response && worktreeId ? refreshAfterArchiveChange(worktreeId, queryClient) : undefined,
  });
}

/**
 * Bind a chat to the project's main checkout instead of its workspace. The
 * workspace itself is kept; only where the chat's tools run changes.
 */
export function useMoveChatToMainCheckout() {
  const queryClient = useQueryClient();
  return useMutation({
    // "" asks the server for the project's main worktree when the caller does
    // not know its id (ResolveChatWorktreeID).
    mutationFn: ({ chatId, mainWorktreeId }: { chatId: string; mainWorktreeId?: string }) =>
      chatActions.updateChat(chatId, { worktree_id: mainWorktreeId ?? "" }),
    onSuccess: (_data, { chatId }) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.detail(chatId) });
    },
  });
}
