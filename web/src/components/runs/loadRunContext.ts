// Copyright (c) 2025 Reliant Labs

/**
 * Put the stores in the state a chat's transcript reads from, for a chat that
 * may belong to any project. The non-navigating half of opening a chat from
 * outside the project view.
 *
 * ChatContainer reads the current project and worktree from stores, not from
 * props, and the chat stream subscribes to the active chat. So rendering one
 * chat's transcript anywhere means: select the chat's project (it may not be
 * the current one — the Runs list spans projects), load that project's
 * worktrees, switch to the chat's worktree, and select the chat.
 *
 * selectChat persists the chat as the worktree's active chat and
 * switchWorktreeContext persists the worktree, so the project view restored
 * later lands on this chat rather than whatever was open before.
 */

import { api, type Chat } from "@/api/client";
import { seedChatDetail } from "@/hooks/chat-queries";
import { useChatStore } from "@/store/chatStore";
import { useChatNavigationStore } from "@/store/chatNavigationStore";
import { useProjectStore } from "@/store/projectStore";
import { useWorktreeStore } from "@/store/worktreeStore";

export class RunProjectUnavailableError extends Error {
  constructor() {
    super("That run's project is no longer available.");
    this.name = "RunProjectUnavailableError";
  }
}

/** Load a chat and make it the active chat in its project. Returns the chat. */
export async function loadRunContext(chatId: string): Promise<Chat> {
  const chat = await api.chatsV2.get(chatId);
  // Seed the detail cache so the transcript does not flash "new chat" while
  // it would otherwise be fetching the chat we already have.
  seedChatDetail(chat);

  const projectStore = useProjectStore.getState();
  if (projectStore.currentProject?.id !== chat.projectId) {
    await projectStore.loadProjects();
    const project = useProjectStore.getState().projects.find((p) => p.id === chat.projectId);
    if (!project) throw new RunProjectUnavailableError();
    await useProjectStore.getState().selectProject(project);
  } else if (useWorktreeStore.getState().worktrees.length === 0) {
    await useWorktreeStore.getState().loadWorktrees(chat.projectId);
  }

  const worktreeStore = useWorktreeStore.getState();
  const worktree = chat.worktreeId
    ? (worktreeStore.worktrees.find((w) => w.id === chat.worktreeId) ?? null)
    : null;
  await worktreeStore.switchWorktreeContext(chat.projectId, worktree);

  useChatStore.getState().selectChat(chat);
  useChatNavigationStore.getState().navigateToChat(chat.id);
  return chat;
}
