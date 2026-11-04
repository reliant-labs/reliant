// Copyright (c) 2025 Reliant Labs

/**
 * Start a chat about an observability subject on a fresh workspace: with the
 * prompt waiting in the composer (`send: false`, "Investigate in chat"), or
 * already sent to an agent (`send: true`, "Fix with agent").
 *
 * A "branched chat" is a chat whose workspace is its own git worktree. With no
 * conversation to branch from, that is: create the workspace (the same store
 * action the New workspace modal calls, so the same toasts, wake handling and
 * settle events) and make it the project's current one.
 *
 * Unsent: the prompt is left in the composer for the user to read and send,
 * through the existing first-send path (NewChatView.handleCreateAndSend →
 * StartChat with that worktree_id).
 *
 * Sent: the chat store's own startChat (the call that first-send path makes),
 * on the new workspace, on the project's default machine; then the chat is
 * opened the way any chat is (openChatInWorkspace). A start that fails falls
 * back to the unsent path, so the prompt is never lost: it is waiting in the
 * composer and `sendError` says why it was not sent. A retry from there sends
 * the same text on the same workspace, which StartChat recognizes by its
 * attempt key, so a start whose response was lost does not make a second chat.
 */

import type { Chat } from "@/api/client";
import { WorktreeStatus } from "@/gen/reliant/v1/worktree_pb";
import { trackEvent } from "@/lib/analytics";
import { logger } from "@/lib/logger";
import { openChatInWorkspace } from "@/lib/openChatInWorkspace";
import { DEFAULT_COPY_PATHS } from "@/lib/worktreeCopyPaths";
import { useChatStore } from "@/store/chatStore";
import { useComposerHandoffStore } from "@/store/composerHandoffStore";
import { useProjectStore, type Project } from "@/store/projectStore";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";
import { createWorktree } from "@/hooks/worktree-actions";
import { useWorktreeStore } from "@/store/worktreeStore";

export interface StartAskReliantChat {
  project: Project;
  prompt: string;
  /** The workspace's name, which is also its branch (askWorkspaceName). */
  workspaceName: string;
  /** Send the prompt now rather than leave it in the composer. */
  send?: boolean;
  /** Called with the chat's id once it exists: at once when sent, else when the user sends. */
  onChatCreated?: (chatId: string) => void;
  /** Move the app to the project's chat screen. */
  openProject: (projectId: string) => void;
}

export interface StartedAskReliantChat {
  worktreeId: string;
  /** The chat, when it was sent. */
  chatId?: string;
  /** Why a `send` was not sent; the prompt is waiting in the composer instead. */
  sendError?: unknown;
}

export async function startAskReliantChat({ project, prompt, workspaceName, send = false, onChatCreated, openProject }: StartAskReliantChat): Promise<StartedAskReliantChat> {
  if (useProjectStore.getState().currentProject?.id !== project.id) {
    await useProjectStore.getState().selectProject(project);
  }

  const worktree = await createWorktree({
    name: workspaceName,
    branch: workspaceName,
    project_id: project.id,
    copy_files: [...DEFAULT_COPY_PATHS],
    status: WorktreeStatus.ACTIVE,
  });

  // The app restores the last workspace when it mounts, and a workspace the
  // store merely holds is not "last" until it is said so.
  useWorkspaceStateStore.getState().setLastWorktree(project.id, worktree.id);

  let sendError: unknown;
  if (send) {
    let chat: Chat | undefined;
    try {
      chat = await useChatStore.getState().startChat(worktree.id, prompt);
    } catch (error) {
      sendError = error;
    }
    if (chat) {
      trackEvent("chat_created", { has_attachments: false, workflow: "default", no_machine: false });
      onChatCreated?.(chat.id);
      // The chat exists whether or not it opens: a failed open is not a reason to offer its prompt again.
      await openChatInWorkspace(chat, { navigate: true }).catch((error: unknown) => logger.warn("[AskReliant] could not open the started chat", { chatId: chat.id, error }));
      openProject(project.id);
      return { worktreeId: worktree.id, chatId: chat.id };
    }
  }

  await useWorktreeStore.getState().switchWorktreeContext(project.id, worktree, { openFreshNewChat: true });
  useComposerHandoffStore.getState().offer({ projectId: project.id, worktreeId: worktree.id, text: prompt, onChatCreated });
  openProject(project.id);
  return sendError === undefined ? { worktreeId: worktree.id } : { worktreeId: worktree.id, sendError };
}
