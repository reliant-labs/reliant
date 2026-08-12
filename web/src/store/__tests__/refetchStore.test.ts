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
    // triggerRefetch debounces for 300ms, so subscribers only see the event
    // once the timer fires.
    vi.useFakeTimers();
    const events: RefetchEvent[] = [];
    const unsubscribe = subscribeToRefetch("worktree_changes", (event) => {
      events.push(event);
    });

    triggerRefetch("worktree_changes", "wt-123");
    vi.advanceTimersByTime(300);

    unsubscribe();

    expect(events).toEqual([
      {
        type: "worktree_changes",
        entityId: "wt-123",
      },
    ]);
    vi.useRealTimers();
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

  it("supports file_tree refetch type", () => {
    vi.useFakeTimers();
    const events: RefetchEvent[] = [];
    const unsubscribe = subscribeToRefetch("file_tree", (event) => {
      events.push(event);
    });
    triggerRefetch("file_tree", "project-123");
    vi.advanceTimersByTime(300);
    unsubscribe();
    expect(events).toEqual([{ type: "file_tree", entityId: "project-123" }]);
    vi.useRealTimers();
  });
});