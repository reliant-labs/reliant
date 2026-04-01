import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  matchesRefetchScope,
  subscribeToRefetch,
  triggerRefetch,
  type RefetchEvent,
} from "../refetchStore";

describe("refetchStore", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("passes entity scope through to subscribers", () => {
    const events: RefetchEvent[] = [];
    const unsubscribe = subscribeToRefetch("worktree_changes", (event) => {
      events.push(event);
    });

    triggerRefetch("worktree_changes", "wt-123");

    unsubscribe();

    expect(events).toEqual([
      {
        type: "worktree_changes",
        entityId: "wt-123",
      },
    ]);
  });

  it("matches worktree-scoped refetches only for the active worktree", () => {
    expect(
      matchesRefetchScope(
        { type: "worktree_changes", entityId: "wt-1" },
        { worktreeId: "wt-1" },
      ),
    ).toBe(true);

    expect(
      matchesRefetchScope(
        { type: "worktree_changes", entityId: "wt-2" },
        { worktreeId: "wt-1" },
      ),
    ).toBe(false);
  });

  it("matches project-scoped refetches when no worktree is active", () => {
    expect(
      matchesRefetchScope(
        { type: "worktree_changes", entityId: "project-1" },
        { projectId: "project-1" },
      ),
    ).toBe(true);

    expect(
      matchesRefetchScope(
        { type: "worktree_changes", entityId: "project-2" },
        { projectId: "project-1" },
      ),
    ).toBe(false);
  });

  it("treats unscoped refetch events as relevant to all subscribers", () => {
    expect(
      matchesRefetchScope(
        { type: "worktree_changes" },
        { worktreeId: "wt-1", projectId: "project-1" },
      ),
    ).toBe(true);
  });
});
