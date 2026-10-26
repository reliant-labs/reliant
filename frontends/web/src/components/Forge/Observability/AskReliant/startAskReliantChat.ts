// Copyright (c) 2025 Reliant Labs

/**
 * Open the app's new-chat screen on a fresh workspace, prompt waiting, NOT sent.
 *
 * A "branched chat" is a chat whose workspace is its own git worktree. With no
 * conversation to branch from, that is: create the workspace (the same store
 * action the New workspace modal calls, so the same toasts, wake handling and
 * settle events), make it the project's current one, and leave the prompt in
 * the composer for the user to read and send. Sending is the existing
 * first-send path (NewChatView.handleCreateAndSend → StartChat with that
 * worktree_id); this adds no StartChat call of its own.
 */

import { WorktreeStatus } from "@/gen/reliant/v1/worktree_pb";
import { DEFAULT_COPY_PATHS } from "@/lib/worktreeCopyPaths";
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
  /** Called with the chat's id when the user sends the first message. */
  onChatCreated?: (chatId: string) => void;
  /** Move the app to the project's chat screen. */
  openProject: (projectId: string) => void;
}

export async function startAskReliantChat({ project, prompt, workspaceName, onChatCreated, openProject }: StartAskReliantChat): Promise<{ worktreeId: string }> {
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
  await useWorktreeStore.getState().switchWorktreeContext(project.id, worktree, { openFreshNewChat: true });

  useComposerHandoffStore.getState().offer({ projectId: project.id, worktreeId: worktree.id, text: prompt, onChatCreated });
  openProject(project.id);
  return { worktreeId: worktree.id };
}
