// Copyright (c) 2025 Reliant Labs

/**
 * How the chat list groups: project → workspace → chat.
 *
 * One definition shared by the desktop sidebar and the navigation store, so the
 * order keyboard next/prev walks is the order the sidebar renders. Pure: the
 * callers supply the chats (already filtered to the active ones), the main
 * workspaces the list carries, and the projects in scope.
 */

import type { WorktreeSummary } from "../gen/reliant/v1/worktree_pb";
import { compareChatGroups, sortChats, type ChatOrderFields, type NeedsAttention } from "./chatListOrder";
import { isWorkspaceClosed, mainWorkspaceOf, workspaceOfChat } from "./chatWorkspace";
import type { ChatSortOption } from "../store/chatListPreferencesStore";

/** The fields of a chat grouping reads. */
export interface GroupableChat extends ChatOrderFields {
  projectId: string;
  worktreeId?: string;
  worktree?: WorktreeSummary;
}

export interface WorkspaceGroup<T extends GroupableChat> {
  worktreeId: string;
  worktreeName: string;
  worktreeBranch: string;
  projectId: string;
  chats: T[];
  isMain: boolean;
}

export interface ProjectGroup<T extends GroupableChat> {
  projectId: string;
  projectName: string;
  workspaces: WorkspaceGroup<T>[];
  /** Every chat of the project, in rendered order. */
  chats: T[];
}

/** The project fields grouping reads. */
export interface GroupableProject {
  id: string;
  name: string;
  last_active?: string;
}

interface GroupOptions<T> {
  sortOrder: ChatSortOption;
  needsAttention: NeedsAttention<T>;
}

/**
 * The chats that are listed as active: a chat on a closed (completed or
 * abandoned) workspace is not. A workspace the server could not find says
 * nothing about the chat, so it stays.
 */
export function activeWorkspaceChats<T extends GroupableChat>(
  chats: readonly T[],
  mainWorktrees: readonly WorktreeSummary[] | undefined,
): T[] {
  return chats.filter((chat) => !isWorkspaceClosed(workspaceOfChat(chat, mainWorktrees)));
}

/**
 * Group one project's chats by workspace. The project's main workspace is
 * always present (first while it has no chats), so a chat can be started there.
 */
export function groupByWorkspace<T extends GroupableChat>(
  projectId: string,
  chats: readonly T[],
  mainWorktrees: readonly WorktreeSummary[] | undefined,
  { sortOrder, needsAttention }: GroupOptions<T>,
): WorkspaceGroup<T>[] {
  const groups = new Map<string, WorkspaceGroup<T>>();
  for (const chat of chats) {
    const worktree = workspaceOfChat(chat, mainWorktrees);
    const groupId = worktree?.id ?? (chat.worktreeId || `no-worktree:${projectId}`);
    let group = groups.get(groupId);
    if (!group) {
      group = {
        worktreeId: groupId,
        worktreeName: worktree?.name ?? "Unknown workspace",
        worktreeBranch: worktree?.branch ?? "Unknown workspace",
        projectId,
        chats: [],
        isMain: worktree?.isMain ?? false,
      };
      groups.set(groupId, group);
    }
    group.chats.push(chat);
  }
  for (const group of groups.values()) {
    group.chats = sortChats([...group.chats], sortOrder, needsAttention);
  }

  const main = mainWorkspaceOf(projectId, mainWorktrees);
  if (main && !main.deletedAt && !isWorkspaceClosed(main) && !groups.has(main.id)) {
    groups.set(main.id, {
      worktreeId: main.id,
      worktreeName: main.name,
      worktreeBranch: main.branch,
      projectId,
      chats: [],
      isMain: true,
    });
  }

  // Groups rank by their leading chat under the selected sort order, so a
  // group only moves when the chat at its top moves.
  return Array.from(groups.values()).sort((a, b) => {
    if (a.isMain && a.chats.length === 0) return -1;
    if (b.isMain && b.chats.length === 0) return 1;
    if (a.chats.length === 0 && b.chats.length > 0) return 1;
    if (b.chats.length === 0 && a.chats.length > 0) return -1;
    if (a.chats.length === 0 && b.chats.length === 0) return a.worktreeId.localeCompare(b.worktreeId);
    return compareChatGroups(a.chats, b.chats, sortOrder, needsAttention);
  });
}

/**
 * Group chats by project, then workspace. Every project in `projects` gets a
 * group, chats or not, so each one can start a chat from the sidebar. Projects
 * with chats rank by their leading chat; the rest follow, most recently used
 * first. A chat whose project is not in `projects` (not loaded yet) still gets
 * a group, named by `fallbackName`.
 */
export function groupByProject<T extends GroupableChat>(
  chats: readonly T[],
  mainWorktrees: readonly WorktreeSummary[] | undefined,
  projects: readonly GroupableProject[],
  options: GroupOptions<T> & { fallbackName?: string },
): ProjectGroup<T>[] {
  const byProject = new Map<string, T[]>();
  for (const project of projects) byProject.set(project.id, []);
  for (const chat of chats) {
    const list = byProject.get(chat.projectId);
    if (list) list.push(chat);
    else byProject.set(chat.projectId, [chat]);
  }
  const names = new Map(projects.map((p) => [p.id, p.name]));
  const lastActive = new Map(projects.map((p) => [p.id, p.last_active ?? ""]));

  const groups = Array.from(byProject.entries()).map(([projectId, projectChats]): ProjectGroup<T> => {
    const workspaces = groupByWorkspace(projectId, projectChats, mainWorktrees, options);
    return {
      projectId,
      projectName: names.get(projectId) ?? options.fallbackName ?? "Project",
      workspaces,
      chats: workspaces.flatMap((w) => w.chats),
    };
  });

  return groups.sort((a, b) => {
    if (a.chats.length > 0 && b.chats.length > 0) {
      return compareChatGroups(a.chats, b.chats, options.sortOrder, options.needsAttention);
    }
    if (a.chats.length > 0) return -1;
    if (b.chats.length > 0) return 1;
    return (
      (lastActive.get(b.projectId) ?? "").localeCompare(lastActive.get(a.projectId) ?? "") ||
      a.projectName.localeCompare(b.projectName)
    );
  });
}
