import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { worktreeGrpc } from "../api/worktree-grpc";
import { chatKeys } from "./chat-queries";

/**
 * Rebuild a chat's workspace directory from its branch when it has gone
 * missing from disk. The server asks the machine that owns it; a workspace
 * that is still there is left untouched.
 */
export function useRecreateWorkspace() {
  return useMutation({
    mutationFn: (worktreeId: string) => worktreeGrpc.recreate(worktreeId),
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
      api.chatsV2.update(chatId, { worktree_id: mainWorktreeId ?? "" }),
    onSuccess: (_data, { chatId }) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.detail(chatId) });
    },
  });
}
