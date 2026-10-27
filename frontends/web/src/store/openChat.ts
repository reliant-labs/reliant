// Copyright (c) 2025 Reliant Labs

/**
 * Open a chat in the main pane, from any project.
 *
 * The one path every "open this chat" goes through — a sidebar row, the tray,
 * a run, a notification — so the context (project, workspace, viewers) always
 * follows the chat being opened. The sidebar does not change: it spans every
 * project, so opening a chat of another project never unmounts the list or
 * the activity of the chats running elsewhere.
 */

import type { Chat } from "../api/client";
import { beginWorkspaceOpen, findProject, openChatInWorkspace, ChatProjectUnavailableError } from "../lib/openChatInWorkspace";
export { findProject, ChatProjectUnavailableError } from "../lib/openChatInWorkspace";
import { getChatFromCache } from "../hooks/chat-queries";
import { resolveMainWorktree, resolveWorktree } from "../hooks/worktree-queries";
import { useChatStore } from "./chatStore";
import { useProjectStore } from "./projectStore";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useWorktreeStore } from "./worktreeStore";


/**
 * Select immediately, then follow its project/workspace with main's identity
 * fence. All source entry points share the same epoch as the main helper.
 */
export function openChat(chat: Chat, isCurrent?: () => boolean): Promise<void> {
  return openChatInWorkspace(chat, { navigate: true, isCurrent });
}

/**
 * The project a global "New chat" starts in: the sidebar's filter when it
 * names one, else the open chat's project (the context project), else the
 * last one used.
 */
export function newChatTargetProjectId(): string | null {
  const filter = useWorkspaceStateStore.getState().projectFilterId;
  if (filter) return filter;
  const context = useProjectStore.getState().currentProject?.id;
  if (context) return context;
  return useWorkspaceStateStore.getState().lastProjectId;
}

/**
 * Show the new-chat view, in `projectId` (default: newChatTargetProjectId)
 * and the workspace `worktreeId` (default: the open chat's, when it is in that
 * project; else the project's main workspace).
 */
export async function openNewChat(target: { projectId?: string | null; worktreeId?: string | null } = {}): Promise<void> {
  const isLatest = beginWorkspaceOpen();
  const projectId = target.projectId ?? newChatTargetProjectId();
  const projectStore = useProjectStore.getState();
  let contextProjectId = projectStore.currentProject?.id;
  const activeChatId = useChatStore.getState().activeChatId;
  const isCurrent = () => isLatest() &&
    useChatStore.getState().activeChatId === activeChatId &&
    useProjectStore.getState().currentProject?.id === contextProjectId;
  const switching = !!projectId && contextProjectId !== projectId;
  if (switching) {
    const project = await findProject(projectId);
    if (!isCurrent()) return;
    if (!project) throw new ChatProjectUnavailableError();
    const selected = useProjectStore.getState().selectProject(project, {
      restoreChat: false,
      restoreWorkspace: false,
      isCurrent: isLatest,
    });
    contextProjectId = project.id;
    await selected;
    if (!isCurrent()) return;
  }
  if (target.worktreeId && contextProjectId) {
    const worktree = await resolveWorktree(target.worktreeId);
    if (!isCurrent()) return;
    if (worktree) {
      await useWorktreeStore.getState().switchWorktreeContext(contextProjectId, worktree, { isCurrent });
    }
    if (!isCurrent()) return;
    useChatStore.getState().clearCurrentChat(target.worktreeId);
    return;
  }

  if (switching) {
    // A new project starts on its main workspace, not on whichever workspace
    // the last chat there used.
    const main = contextProjectId ? await resolveMainWorktree(contextProjectId) : null;
    if (!isCurrent()) return;
    if (main && contextProjectId) {
      await useWorktreeStore.getState().switchWorktreeContext(contextProjectId, main, { isCurrent });
    }
    if (!isCurrent()) return;
    useChatStore.getState().clearCurrentChat(main?.id ?? null);
    return;
  }

  // Same project: keep the open chat's workspace.
  const chatStore = useChatStore.getState();
  const activeChat = chatStore.activeChatId ? getChatFromCache(chatStore.activeChatId) : undefined;
  const worktreeId = activeChat?.worktreeId || useWorktreeStore.getState().currentWorktreeId || null;
  chatStore.clearCurrentChat(worktreeId);
}
