import { useCallback, useState } from "react";
import { usePreferences } from "../../hooks/settings-queries";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { useChatStore } from "../../store/chatStore";
import { useChatNavigationStore } from "../../store/chatNavigationStore";
import type { Chat } from "../../types/chat";

type CleanupOptions = { deleteGitBranch: boolean };

/**
 * Archive a workspace the way the user's cleanup settings say to.
 *
 * "Ask every time" stages the workspace for the confirmation dialog (render
 * DeleteWorktreeModal from `pending`); the other two modes archive at once
 * with the saved defaults. One copy of this rule, shared by the row action
 * and the detail view, so the two cannot drift.
 */
export function useArchiveWorkspace() {
  const deleteWorktree = useWorktreeStore((state) => state.deleteWorktree);
  const { data: preferences } = usePreferences();
  const [pending, setPending] = useState<Worktree | null>(null);

  const requestArchive = useCallback(
    (worktree: Worktree) => {
      if (worktree.is_main) return;
      const mode = preferences?.worktree.archiveMode ?? "ask_me";
      if (mode === "ask_me") {
        setPending(worktree);
        return;
      }
      const options: CleanupOptions =
        mode === "always_cleanup"
          ? { deleteGitBranch: preferences?.worktree.defaultDeleteBranch ?? false }
          : { deleteGitBranch: false };
      void deleteWorktree(worktree.id, options);
    },
    [deleteWorktree, preferences],
  );

  const confirm = useCallback(
    async (options?: CleanupOptions) => {
      if (pending) await deleteWorktree(pending.id, options);
    },
    [deleteWorktree, pending],
  );

  return { requestArchive, pending, confirm, cancel: () => setPending(null) };
}

/**
 * Leave the workspace list and go work in a workspace, or in one of its chats.
 * `onAfterOpen` lets a host that sits in front of the app (Settings) get out
 * of the way once the switch has happened.
 */
export function useOpenWorkspace(onAfterOpen?: () => void) {
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);

  const openWorkspace = useCallback(
    async (worktree: Worktree) => {
      const projectId = useProjectStore.getState().currentProject?.id;
      if (!projectId) return;
      await switchWorktreeContext(projectId, worktree);
      onAfterOpen?.();
    },
    [switchWorktreeContext, onAfterOpen],
  );

  const openChat = useCallback(
    async (chat: Chat) => {
      const projectId = useProjectStore.getState().currentProject?.id;
      if (!projectId) return;
      const worktree = chat.worktreeId
        ? useWorktreeStore.getState().worktrees.find((w) => w.id === chat.worktreeId) ?? null
        : null;
      await switchWorktreeContext(projectId, worktree);
      useChatStore.getState().selectChat(chat);
      useChatNavigationStore.getState().navigateToChat(chat.id);
      onAfterOpen?.();
    },
    [switchWorktreeContext, onAfterOpen],
  );

  return { openWorkspace, openChat };
}
