import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import type { WorktreeGitStatus } from "../../api/worktree-grpc";
import type { Worktree } from "../../store/worktreeStore";
import type { Chat } from "../../types/chat";

/** Mirrors forge-ui StatusDot's variants, which that module does not export. */
export type StatusVariant = "active" | "paused" | "pending" | "error" | "warning" | "neutral";

export interface LifecycleInfo {
  label: string;
  description: string;
  variant: StatusVariant;
}

/**
 * Where the WORK in a workspace stands, as the user marked it. Distinct from
 * the working tree's git state (see workingTreeState): a workspace can be
 * "Merging" with a clean tree, or "Active" with uncommitted changes.
 */
const LIFECYCLE: Record<WorktreeStatus, LifecycleInfo> = {
  [WorktreeStatus.UNSPECIFIED]: {
    label: "Unknown",
    description: "No status has been recorded",
    variant: "neutral",
  },
  [WorktreeStatus.ACTIVE]: {
    label: "Active",
    description: "Currently being worked on",
    variant: "active",
  },
  [WorktreeStatus.MERGING]: {
    label: "Merging",
    description: "Pull request in review or merging",
    variant: "pending",
  },
  [WorktreeStatus.COMPLETED]: {
    label: "Completed",
    description: "Work finished and merged",
    variant: "neutral",
  },
  [WorktreeStatus.ABANDONED]: {
    label: "Abandoned",
    description: "No longer being worked on",
    variant: "paused",
  },
  [WorktreeStatus.CREATING]: {
    label: "Creating",
    description: "The worktree is still being created on your machine",
    variant: "pending",
  },
  [WorktreeStatus.FAILED]: {
    label: "Failed",
    description: "Creating the worktree did not finish",
    variant: "error",
  },
};

/**
 * Whether a worktree exists on disk to ask git about. A CREATING row has no
 * checkout yet and a FAILED one may never get one, so their git status is not
 * a question worth a round trip to the machine.
 */
export function hasCheckout(worktree: Pick<Worktree, "status">): boolean {
  return worktree.status !== WorktreeStatus.CREATING && worktree.status !== WorktreeStatus.FAILED;
}

export const LIFECYCLE_OPTIONS: WorktreeStatus[] = [
  WorktreeStatus.ACTIVE,
  WorktreeStatus.MERGING,
  WorktreeStatus.COMPLETED,
  WorktreeStatus.ABANDONED,
];

export function lifecycleOf(status: WorktreeStatus): LifecycleInfo {
  return LIFECYCLE[status] ?? LIFECYCLE[WorktreeStatus.UNSPECIFIED];
}

export interface WorkingTreeState {
  variant: StatusVariant;
  /** Short cell text: "Clean", "3 uncommitted", "2 ahead". */
  label: string;
  /** One sentence for a tooltip. */
  detail: string;
}

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

/**
 * Collapse a git status into the one fact a list row should show. Uncommitted
 * changes win over ahead/behind, because they are the thing that is lost if
 * the workspace is cleaned up.
 */
export function workingTreeState(status: WorktreeGitStatus): WorkingTreeState {
  const staged = status.staged_files?.length ?? 0;
  const modified = status.modified_files?.length ?? 0;
  const untracked = status.untracked_files?.length ?? 0;
  const changes = staged + modified + untracked;

  if (changes > 0 || !status.is_clean) {
    const parts = [
      modified > 0 && `${modified} modified`,
      staged > 0 && `${staged} staged`,
      untracked > 0 && `${untracked} untracked`,
    ].filter(Boolean);
    return {
      variant: "warning",
      label: changes > 0 ? `${changes} uncommitted` : "Uncommitted changes",
      detail:
        parts.length > 0
          ? `Uncommitted changes: ${parts.join(", ")}.`
          : "The working tree has uncommitted changes.",
    };
  }

  if (status.ahead > 0 || status.behind > 0) {
    const label = [
      status.ahead > 0 && `${status.ahead} ahead`,
      status.behind > 0 && `${status.behind} behind`,
    ]
      .filter(Boolean)
      .join(", ");
    const detail = [
      status.ahead > 0 && `${plural(status.ahead, "commit")} not yet pushed`,
      status.behind > 0 && `${plural(status.behind, "commit")} to pull`,
    ]
      .filter(Boolean)
      .join("; ");
    return {
      variant: status.ahead > 0 ? "pending" : "neutral",
      label,
      detail: `Clean working tree. ${detail}.`,
    };
  }

  return {
    variant: "active",
    label: "Clean",
    detail: "No uncommitted changes, and in sync with its upstream branch.",
  };
}

function timeOf(value: string | undefined | null): number {
  if (!value) return 0;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
}

/** Chats that run in a workspace, most recently used first. */
export function chatsForWorkspace(chats: Chat[], worktreeId: string): Chat[] {
  return chats
    .filter((chat) => chat.worktreeId === worktreeId)
    .sort(
      (a, b) =>
        timeOf(b.lastMessageAt || b.updatedAt || b.createdAt) -
        timeOf(a.lastMessageAt || a.updatedAt || a.createdAt),
    );
}

/** Main checkout first, then most recently active. */
export function sortActiveWorkspaces(worktrees: Worktree[]): Worktree[] {
  return [...worktrees].sort((a, b) => {
    if (a.is_main !== b.is_main) return a.is_main ? -1 : 1;
    return timeOf(b.last_active) - timeOf(a.last_active);
  });
}

/** Most recently archived first. */
export function sortArchivedWorkspaces(worktrees: Worktree[]): Worktree[] {
  return [...worktrees].sort((a, b) => timeOf(b.deleted_at) - timeOf(a.deleted_at));
}

/** What cleanup removed when the workspace was archived, in plain words. */
export function cleanupSummary(worktree: Worktree): string {
  const meta = worktree.cleanup_metadata;
  if (!meta) return "Files kept";
  const files = meta.directory_deleted ? "Files removed" : "Files kept";
  return meta.branch_deleted ? `${files}, branch deleted` : files;
}
