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
import { fetchProjects, getCachedProjects, type Project } from "../hooks/project-queries";
import { getChatFromCache } from "../hooks/chat-queries";
import { resolveMainWorktree, resolveWorktree } from "../hooks/worktree-queries";
import { useChatNavigationStore } from "./chatNavigationStore";
import { useChatStore } from "./chatStore";
import { useProjectStore } from "./projectStore";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useWorktreeStore } from "./worktreeStore";

export class ChatProjectUnavailableError extends Error {
  constructor() {
    super("That chat's project is no longer available.");
    this.name = "ChatProjectUnavailableError";
  }
}

/** The project row for an id: the cached list, else a fresh read. */
export async function findProject(projectId: string): Promise<Project | undefined> {
  const cached = getCachedProjects().find((p) => p.id === projectId);
  if (cached) return cached;
  return (await fetchProjects()).find((p) => p.id === projectId);
}

/**
 * Make `chat` the open chat: switch the context project to the chat's (without
 * reopening that project's last chat), switch to the chat's workspace, then
 * select it. Throws ChatProjectUnavailableError when the chat's project is
 * gone.
 */
export async function openChat(chat: Chat): Promise<void> {
  const projectStore = useProjectStore.getState();
  if (chat.projectId && projectStore.currentProject?.id !== chat.projectId) {
    const project = await findProject(chat.projectId);
    if (!project) throw new ChatProjectUnavailableError();
    await useProjectStore.getState().selectProject(project, { restoreChat: false });
  }

  const projectId = chat.projectId || useProjectStore.getState().currentProject?.id;
  if (projectId) {
    const worktree = chat.worktreeId ? await resolveWorktree(chat.worktreeId) : null;
    // A workspace row that is gone leaves the context where it is; the chat
    // still opens.
    if (worktree || !chat.worktreeId) {
      await useWorktreeStore.getState().switchWorktreeContext(projectId, worktree ?? null);
    }
  }

  useChatStore.getState().selectChat(chat);
  useChatNavigationStore.getState().navigateToChat(chat.id);
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
  const projectId = target.projectId ?? newChatTargetProjectId();
  const projectStore = useProjectStore.getState();
  const switching = !!projectId && projectStore.currentProject?.id !== projectId;
  if (switching) {
    const project = await findProject(projectId);
    if (project) {
      await useProjectStore.getState().selectProject(project, { restoreChat: false });
    }
  }

  const contextProjectId = useProjectStore.getState().currentProject?.id;
  if (target.worktreeId && contextProjectId) {
    const worktree = await resolveWorktree(target.worktreeId);
    if (worktree) {
      await useWorktreeStore.getState().switchWorktreeContext(contextProjectId, worktree);
    }
    useChatStore.getState().clearCurrentChat(target.worktreeId);
    return;
  }

  if (switching) {
    // A new project starts on its main workspace, not on whichever workspace
    // the last chat there used.
    const main = contextProjectId ? await resolveMainWorktree(contextProjectId) : null;
    if (main && contextProjectId) {
      await useWorktreeStore.getState().switchWorktreeContext(contextProjectId, main);
    }
    useChatStore.getState().clearCurrentChat(main?.id ?? null);
    return;
  }

  // Same project: keep the open chat's workspace.
  const chatStore = useChatStore.getState();
  const activeChat = chatStore.activeChatId ? getChatFromCache(chatStore.activeChatId) : undefined;
  const worktreeId = activeChat?.worktreeId || useWorktreeStore.getState().currentWorktreeId || null;
  chatStore.clearCurrentChat(worktreeId);
}
