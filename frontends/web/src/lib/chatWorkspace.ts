import { WorktreeStatus } from "../gen/reliant/v1/worktree_pb";
import type { WorktreeSummary } from "../gen/reliant/v1/worktree_pb";
import type { Chat } from "../types/chat";

/**
 * The workspace a chat runs in, as the chat list carries it. The sidebar groups
 * chats by this, so it never needs the project's full worktree list: what it
 * costs to show chats scales with the chats, not with every workspace an agent
 * ever created.
 */
export type ChatWorkspace = WorktreeSummary;

/**
 * The main workspace of a project, from the main workspaces a chat list
 * carries (one per project in its scope).
 */
export function mainWorkspaceOf(
  projectId: string | undefined,
  mainWorktrees: readonly WorktreeSummary[] | undefined,
): WorktreeSummary | undefined {
  if (!projectId || !mainWorktrees) return undefined;
  return mainWorktrees.find((w) => w.projectId === projectId);
}

/**
 * The workspace a chat belongs to. A chat with no worktree_id runs in its
 * project's main workspace. Undefined only when the server named a worktree it
 * could not find (its row is gone) — callers keep the chat and group it under
 * an "Unknown workspace" fallback rather than hiding it.
 */
export function workspaceOfChat(
  chat: Pick<Chat, "worktreeId" | "worktree" | "projectId">,
  mainWorktrees: readonly WorktreeSummary[] | undefined,
): WorktreeSummary | undefined {
  if (chat.worktree) return chat.worktree;
  if (!chat.worktreeId) return mainWorkspaceOf(chat.projectId, mainWorktrees);
  return undefined;
}

/** A workspace whose work is done: its chats are not listed as active. */
export function isWorkspaceClosed(workspace: Pick<WorktreeSummary, "status"> | undefined): boolean {
  return (
    workspace?.status === WorktreeStatus.COMPLETED ||
    workspace?.status === WorktreeStatus.ABANDONED
  );
}
