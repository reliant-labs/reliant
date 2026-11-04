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
import { fetchProjects, getCachedProjects, type Project } from "../hooks/project-queries";

export class ChatProjectUnavailableError extends Error {
  constructor() {
    super("That chat's project is no longer available.");
    this.name = "ChatProjectUnavailableError";
  }
}

export async function findProject(projectId: string): Promise<Project | undefined> {
  return getCachedProjects().find((p) => p.id === projectId) ??
    (await fetchProjects()).find((p) => p.id === projectId);
}

export interface OpenChatOptions {
  /** Also move the chat to the front of the navigation queue (a sidebar click). */
  navigate?: boolean;
  /** Continue an intent that began before resolving the chat (notifications). */
  isCurrent?: () => boolean;
}

// Identity alone is insufficient when A is opened again before an older A
// finishes. Only the newest open may commit selection-dependent state.
let openEpoch = 0;

/** A new-chat intent supersedes pending chat opens too (including A → B → A). */
export function beginWorkspaceOpen(): () => boolean {
  const epoch = ++openEpoch;
  return () => epoch === openEpoch;
}

/**
 * Resolves once the chat is selected AND its worktree context is current and
 * recorded. Selection itself happens before the first await.
 */
export async function openChatInWorkspace(
  chat: Chat,
  options: OpenChatOptions = {}
): Promise<void> {
  const isLatest = options.isCurrent ?? beginWorkspaceOpen();
  if (!isLatest()) return;
  let projectId = useProjectStore.getState().currentProject?.id;
  // Selection starts the chat stream immediately, even while its project row
  // is loading. Persistence waits for the matching project/workspace below.
  useChatStore.getState().selectChat(chat, { deferPersist: true });

  // Pass the fence into the switch too: it must recheck after its own import,
  // BEFORE it writes viewer, navigation or workspace routing state.
  const isCurrent = () =>
    isLatest() &&
    useChatStore.getState().activeChatId === chat.id &&
    useProjectStore.getState().currentProject?.id === projectId;

  if (chat.projectId && chat.projectId !== projectId) {
    const project = await findProject(chat.projectId);
    if (!isCurrent()) return;
    if (!project) throw new ChatProjectUnavailableError();
    // selectProject commits synchronously in this mode, without restoring a
    // remembered chat/worktree over this open or starting unfenced async work.
    const selected = useProjectStore.getState().selectProject(project, {
      restoreChat: false,
      restoreWorkspace: false,
      isCurrent: isLatest,
    });
    projectId = project.id;
    await selected;
    if (!isCurrent()) return;
  }

  if (projectId) {
    const worktrees = useWorktreeStore.getState();
    if (chat.worktreeId) {
      const worktree = await resolveWorktree(chat.worktreeId);
      if (!isCurrent()) return;
      if (worktree) {
        await worktrees.switchWorktreeContext(projectId, worktree, { isCurrent });
      }
    } else {
      await worktrees.switchWorktreeContext(projectId, null, { isCurrent });
    }
  }

  if (!isCurrent()) return;
  useChatStore.getState().persistActiveChat(chat.id);
  if (options.navigate) {
    useChatNavigationStore.getState().navigateToChat(chat.id);
  }
}
