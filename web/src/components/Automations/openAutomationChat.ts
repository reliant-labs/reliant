// Copyright (c) 2025 Reliant Labs

/**
 * Open a chat an automation launched.
 *
 * A chat opens inside the project view (ModernApp, mounted at
 * /project/$projectId), and the active chat is store state rather than a URL.
 * So "open this chat" from a page outside the app shell is the same sequence
 * the sidebar and the notification handler run (Sidebar.handleChatClick,
 * globalUpdatesStore's chat focus), plus two steps they never need:
 *
 *   1. The chat may belong to a DIFFERENT project than the current one — an
 *      automation list spans every project — so select that project first.
 *   2. Then navigate into the project view. selectChat persists the chat as
 *      the worktree's active chat and switchWorktreeContext persists the
 *      worktree, so ModernApp's workspace restore on mount lands on this chat
 *      instead of replacing it with whatever was open before.
 */

import { api } from "@/api/client";
import { seedChatDetail } from "@/hooks/chat-queries";
import { useChatStore } from "@/store/chatStore";
import { useChatNavigationStore } from "@/store/chatNavigationStore";
import { useProjectStore } from "@/store/projectStore";
import { useWorktreeStore } from "@/store/worktreeStore";

type NavigateToProject = (projectId: string) => void | Promise<void>;

export async function openAutomationChat(
  chatId: string,
  projectId: string,
  navigateToProject: NavigateToProject,
): Promise<void> {
  const projectStore = useProjectStore.getState();
  if (projectStore.currentProject?.id !== projectId) {
    await projectStore.loadProjects();
    const project = useProjectStore.getState().projects.find((p) => p.id === projectId);
    if (!project) throw new Error("That automation's project is no longer available.");
    await useProjectStore.getState().selectProject(project);
  } else if (useWorktreeStore.getState().worktrees.length === 0) {
    await useWorktreeStore.getState().loadWorktrees(projectId);
  }

  const chat = await api.chatsV2.get(chatId);
  // Seed the detail cache so the chat view does not flash "new chat" while it
  // would otherwise be fetching the chat we already have.
  seedChatDetail(chat);

  const worktreeStore = useWorktreeStore.getState();
  const worktree = chat.worktreeId
    ? (worktreeStore.worktrees.find((w) => w.id === chat.worktreeId) ?? null)
    : null;
  await worktreeStore.switchWorktreeContext(projectId, worktree);

  useChatStore.getState().selectChat(chat);
  useChatNavigationStore.getState().navigateToChat(chat.id);

  await navigateToProject(projectId);
}
