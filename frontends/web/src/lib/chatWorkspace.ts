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

/** The list's main workspace plus the project id it belongs to is all a grouping needs. */
export interface ChatListWorkspaces {
  mainWorktree?: WorktreeSummary;
}

/**
 * The workspace a chat belongs to. A chat with no worktree_id runs in the
 * project's main workspace. Undefined only when the server named a worktree it
 * could not find (its row is gone) — callers keep the chat and group it under
 * an "Unknown workspace" fallback rather than hiding it.
 */
export function workspaceOfChat(
  chat: Pick<Chat, "worktreeId" | "worktree">,
  mainWorktree: WorktreeSummary | undefined,
): WorktreeSummary | undefined {
  if (chat.worktree) return chat.worktree;
  if (!chat.worktreeId) return mainWorktree;
  return undefined;
}

/** A workspace whose work is done: its chats are not listed as active. */
export function isWorkspaceClosed(workspace: Pick<WorktreeSummary, "status"> | undefined): boolean {
  return (
    workspace?.status === WorktreeStatus.COMPLETED ||
    workspace?.status === WorktreeStatus.ABANDONED
  );
}
