import { describe, expect, it } from "vitest";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import type { Worktree } from "../../../store/worktreeStore";
import type { Chat } from "../../../types/chat";
import {
  chatsForWorkspace,
  cleanupSummary,
  hasCheckout,
  lifecycleOf,
  sortActiveWorkspaces,
  workingTreeState,
} from "../workspaceStatus";

const cleanStatus = {
  is_clean: true,
  current_branch: "feature",
  ahead: 0,
  behind: 0,
  staged_files: [],
  modified_files: [],
  untracked_files: [],
  has_remote: false,
};

function worktree(overrides: Partial<Worktree>): Worktree {
  return {
    id: "wt",
    name: "wt",
    path: "/tmp/wt",
    branch: "feature",
    base_branch: "main",
    status: WorktreeStatus.ACTIVE,
    is_main: false,
    created_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
    last_active: "2025-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("workingTreeState", () => {
  it("reports a clean, in-sync tree as clean", () => {
    expect(workingTreeState(cleanStatus)).toMatchObject({ variant: "active", label: "Clean" });
  });

  it("puts uncommitted changes ahead of commits to push, since cleanup loses them", () => {
    const state = workingTreeState({
      ...cleanStatus,
      is_clean: false,
      ahead: 3,
      modified_files: ["a.ts", "b.ts"],
      untracked_files: ["c.ts"],
    });
    expect(state.variant).toBe("warning");
    expect(state.label).toBe("3 uncommitted");
    expect(state.detail).toContain("2 modified");
    expect(state.detail).toContain("1 untracked");
  });

  it("reports commits ahead of upstream on a clean tree", () => {
    const state = workingTreeState({ ...cleanStatus, ahead: 2 });
    expect(state).toMatchObject({ variant: "pending", label: "2 ahead" });
    expect(state.detail).toContain("2 commits not yet pushed");
  });
});

describe("hasCheckout", () => {
  it("is false while a worktree is being created or after creation failed", () => {
    expect(hasCheckout({ status: WorktreeStatus.CREATING })).toBe(false);
    expect(hasCheckout({ status: WorktreeStatus.FAILED })).toBe(false);
    expect(hasCheckout({ status: WorktreeStatus.ACTIVE })).toBe(true);
  });

  it("gives every lifecycle state a label, including the ones the old UI called Unknown", () => {
    expect(lifecycleOf(WorktreeStatus.CREATING).label).toBe("Creating");
    expect(lifecycleOf(WorktreeStatus.FAILED).variant).toBe("error");
  });
});

describe("ordering", () => {
  it("lists the main checkout first, then the most recently active", () => {
    const sorted = sortActiveWorkspaces([
      worktree({ id: "old", last_active: "2025-01-01T00:00:00Z" }),
      worktree({ id: "main", is_main: true, last_active: "2024-01-01T00:00:00Z" }),
      worktree({ id: "new", last_active: "2025-06-01T00:00:00Z" }),
    ]);
    expect(sorted.map((w) => w.id)).toEqual(["main", "new", "old"]);
  });

  it("returns a workspace's chats newest first", () => {
    const chats = [
      { id: "a", worktreeId: "wt", updatedAt: "2025-01-01T00:00:00Z", createdAt: "" },
      { id: "b", worktreeId: "other", updatedAt: "2025-09-01T00:00:00Z", createdAt: "" },
      { id: "c", worktreeId: "wt", lastMessageAt: "2025-05-01T00:00:00Z", updatedAt: "", createdAt: "" },
    ] as unknown as Chat[];
    expect(chatsForWorkspace(chats, "wt").map((c) => c.id)).toEqual(["c", "a"]);
  });
});

describe("cleanupSummary", () => {
  it("says what archive cleanup left on disk", () => {
    expect(cleanupSummary(worktree({ cleanup_metadata: null }))).toBe("Files kept");
    expect(
      cleanupSummary(
        worktree({ cleanup_metadata: { directory_deleted: true, branch_deleted: true } }),
      ),
    ).toBe("Files removed, branch deleted");
  });
});
