/**
 * The file tree refreshes on pushed file-change events, and only on the ones
 * worth a GetFileTree: about the tree on screen, while it is visible. A change
 * missed while hidden must still land when the tree is shown again — the
 * point is to stop paying for updates nobody sees, never to show a stale tree.
 */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { triggerRefetch } from "../../store/refetchStore";
import { fileTreeEventInScope, useFileTreePushRefresh } from "./useFileTreePushRefresh";

const PROJECT = "project-1";
const WORKTREE = "worktree-main";

// refetchStore debounces each (type, entity) by 300ms before notifying.
async function deliver(type: "file_tree" | "worktree_changes", entityId?: string) {
  triggerRefetch(type, entityId);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(350);
  });
}

function setDocumentHidden(hidden: boolean) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => (hidden ? "hidden" : "visible"),
  });
  document.dispatchEvent(new Event("visibilitychange"));
}

function mount(initial: { visible: boolean; worktreeId?: string }, refresh = vi.fn()) {
  const view = renderHook(
    (props: { visible: boolean; worktreeId?: string }) =>
      useFileTreePushRefresh({
        projectId: PROJECT,
        worktreeId: props.worktreeId ?? WORKTREE,
        visible: props.visible,
        refresh,
      }),
    { initialProps: initial },
  );
  return { ...view, refresh };
}

beforeEach(() => {
  vi.useFakeTimers();
  setDocumentHidden(false);
});

afterEach(() => {
  vi.useRealTimers();
  setDocumentHidden(false);
});

describe("fileTreeEventInScope", () => {
  const scope = { projectId: PROJECT, worktreeId: WORKTREE };

  it("accepts this worktree, this project, and unscoped events", () => {
    expect(fileTreeEventInScope({ type: "worktree_changes", entityId: WORKTREE }, scope)).toBe(true);
    expect(fileTreeEventInScope({ type: "file_tree", entityId: PROJECT }, scope)).toBe(true);
    expect(fileTreeEventInScope({ type: "file_tree" }, scope)).toBe(true);
  });

  it("rejects another worktree's or project's events", () => {
    expect(fileTreeEventInScope({ type: "worktree_changes", entityId: "worktree-other" }, scope)).toBe(false);
    expect(fileTreeEventInScope({ type: "file_tree", entityId: "project-other" }, scope)).toBe(false);
  });
});

describe("useFileTreePushRefresh", () => {
  it("refreshes on an agent edit in this worktree and on a project file change", async () => {
    const { refresh } = mount({ visible: true });

    await deliver("worktree_changes", WORKTREE);
    expect(refresh).toHaveBeenCalledTimes(1);

    await deliver("file_tree", PROJECT);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("ignores edits in other workspaces", async () => {
    const { refresh } = mount({ visible: true });

    await deliver("worktree_changes", "worktree-other");
    await deliver("file_tree", "project-other");

    expect(refresh).not.toHaveBeenCalled();
  });

  it("defers changes while the Files tab is hidden, then catches up once", async () => {
    const { refresh, rerender } = mount({ visible: false });

    await deliver("worktree_changes", WORKTREE);
    await deliver("file_tree", PROJECT);
    expect(refresh).not.toHaveBeenCalled();

    rerender({ visible: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refresh).toHaveBeenCalledTimes(1);

    // Nothing more was missed, so re-showing it again costs nothing.
    rerender({ visible: false });
    rerender({ visible: true });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("defers changes while the window is hidden, then catches up when it is shown", async () => {
    const { refresh } = mount({ visible: true });

    setDocumentHidden(true);
    await deliver("worktree_changes", WORKTREE);
    expect(refresh).not.toHaveBeenCalled();

    await act(async () => {
      setDocumentHidden(false);
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("does not replay a hidden worktree's missed change after switching worktrees", async () => {
    const { refresh, rerender } = mount({ visible: false });

    await deliver("worktree_changes", WORKTREE);
    // A worktree switch is a fresh load of its own; the old miss is moot.
    rerender({ visible: false, worktreeId: "worktree-branch" });
    rerender({ visible: true, worktreeId: "worktree-branch" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

    expect(refresh).not.toHaveBeenCalled();
  });

  it("collapses events that arrive during a refresh into one follow-up", async () => {
    let finish!: () => void;
    const refresh = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    mount({ visible: true }, refresh);

    await deliver("worktree_changes", WORKTREE);
    expect(refresh).toHaveBeenCalledTimes(1);

    // Three more edits land while the first refresh is still in flight.
    await deliver("worktree_changes", WORKTREE);
    await deliver("file_tree", PROJECT);
    await deliver("worktree_changes", WORKTREE);
    expect(refresh).toHaveBeenCalledTimes(1);

    await act(async () => {
      finish();
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refresh).toHaveBeenCalledTimes(2);

    await act(async () => {
      finish();
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(refresh).toHaveBeenCalledTimes(2);
  });
});
