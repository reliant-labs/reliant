/**
 * Open a chat: show it, and bring the workspace it lives in along.
 *
 * Opening used to resolve the chat's worktree (a GetWorktree RPC when the
 * store does not hold it), switch the worktree context, and only then select
 * the chat. The chat stream subscribes inside selectChat, so every open of a
 * branch chat paid that RPC before the transcript's stream could even start.
 *
 * The chat does not need the worktree to load: its messages, detail and
 * stream are keyed by chat id. So selection goes first and starts the
 * subscribe immediately, while the worktree resolves and the context switches
 * beside it. What does depend on the worktree context waits for it:
 *
 *  - recording the chat as the worktree's active chat and in its recency
 *    queue (chatStore.persistActiveChat) — both are stored under the CURRENT
 *    worktree, which must already be the chat's;
 *  - navigateToChat — switchWorktreeContext restores the target worktree's
 *    stored queue over the live one, so a queue entry added before the switch
 *    would be overwritten by it.
 */

import type { Chat } from "../api/client";
import { useChatNavigationStore } from "../store/chatNavigationStore";
import { useChatStore } from "../store/chatStore";
import { useProjectStore } from "../store/projectStore";
import { resolveWorktree } from "../hooks/worktree-queries";
import { useWorktreeStore } from "../store/worktreeStore";

export interface OpenChatOptions {
  /** Also move the chat to the front of the navigation queue (a sidebar click). */
  navigate?: boolean;
}

/**
 * Resolves once the chat is selected AND its worktree context is current and
 * recorded. Selection itself happens before the first await.
 */
export async function openChatInWorkspace(
  chat: Chat,
  options: OpenChatOptions = {}
): Promise<void> {
  useChatStore.getState().selectChat(chat, { deferPersist: true });

  // Another chat was opened (or the chat view left) while the worktree
  // resolved: that open owns the context now, and switching to this chat's
  // worktree would put the user in the wrong workspace for the chat on screen.
  const superseded = () => useChatStore.getState().activeChatId !== chat.id;

  const projectId = useProjectStore.getState().currentProject?.id;
  if (projectId) {
    const worktrees = useWorktreeStore.getState();
    if (chat.worktreeId) {
      const worktree = await resolveWorktree(chat.worktreeId);
      if (superseded()) return;
      if (worktree) {
        await worktrees.switchWorktreeContext(projectId, worktree);
      }
    } else {
      await worktrees.switchWorktreeContext(projectId, null);
    }
    if (superseded()) return;
  }

  useChatStore.getState().persistActiveChat(chat.id);
  if (options.navigate) {
    useChatNavigationStore.getState().navigateToChat(chat.id);
  }
}
