/**
 * The project's workspace list is on demand and paged: one page per request,
 * main first, the next page only when asked; archived workspaces are a
 * separate read; and the selected workspace stays in a picker's list even when
 * it sits beyond the loaded pages.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useWorktreeStore } from "@/store/worktreeStore";
import { selectWorktreeForTest } from "@/test/worktreeCache";
import { worktreeServer } from "@/test/worktreeServer";
import { useArchivedWorktreeList, useWorktreeList, WORKTREE_PAGE_SIZE } from "../worktree-queries";

const projectId = "project-1";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/** Main plus `count` agent workspaces, one more page than fits. */
function manyWorkspaces(count: number) {
  return [
    { id: "wt-main", project_id: projectId, is_main: true },
    ...Array.from({ length: count }, (_, i) => ({ id: `wt-${i}`, project_id: projectId })),
  ];
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
  useWorktreeStore.getState().reset();
});

describe("useWorktreeList", () => {
  it("loads one page, main first, and fetches the next only when asked, ending at the last", async () => {
    const server = worktreeServer(manyWorkspaces(WORKTREE_PAGE_SIZE + 5));
    setTransport(mockTransport(server.handlers));

    const { result } = renderHook(() => useWorktreeList(projectId), { wrapper });
    await waitFor(() => expect(result.current.worktrees).toHaveLength(WORKTREE_PAGE_SIZE));
    expect(result.current.worktrees[0].id).toBe("wt-main");
    expect(result.current.hasMore).toBe(true);
    expect(server.requests("ListWorktrees")).toEqual([expect.objectContaining({ limit: WORKTREE_PAGE_SIZE })]);

    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.worktrees).toHaveLength(WORKTREE_PAGE_SIZE + 6));
    expect(result.current.hasMore).toBe(false);
    expect(server.requests("ListWorktrees")[1].pageToken).toBe(String(WORKTREE_PAGE_SIZE));
  });

  it("shares one request between every mounted picker", async () => {
    const server = worktreeServer(manyWorkspaces(2));
    setTransport(mockTransport(server.handlers));

    const a = renderHook(() => useWorktreeList(projectId), { wrapper });
    const b = renderHook(() => useWorktreeList(projectId), { wrapper });
    await waitFor(() => {
      expect(a.result.current.worktrees).toHaveLength(3);
      expect(b.result.current.worktrees).toHaveLength(3);
    });
    expect(server.calls("ListWorktrees")).toBe(1);
  });

  it("keeps the selected workspace when it lies beyond the first page", async () => {
    const server = worktreeServer(manyWorkspaces(WORKTREE_PAGE_SIZE + 5));
    setTransport(mockTransport(server.handlers));
    selectWorktreeForTest({ id: `wt-${WORKTREE_PAGE_SIZE + 2}`, project_id: projectId });

    const { result } = renderHook(() => useWorktreeList(projectId), { wrapper });
    await waitFor(() => expect(result.current.worktrees).toHaveLength(WORKTREE_PAGE_SIZE + 1));
    expect(result.current.worktrees.at(-1)?.id).toBe(`wt-${WORKTREE_PAGE_SIZE + 2}`);
  });

  it("fetches nothing without a project or while disabled", async () => {
    const server = worktreeServer(manyWorkspaces(1));
    setTransport(mockTransport(server.handlers));
    renderHook(() => useWorktreeList(undefined), { wrapper });
    renderHook(() => useWorktreeList(projectId, { enabled: false }), { wrapper });
    await new Promise((r) => setTimeout(r, 20));
    expect(server.calls("ListWorktrees")).toBe(0);
  });
});

describe("archived workspaces are a separate read", () => {
  it("never enter the live list, and load only when asked", async () => {
    const server = worktreeServer([
      { id: "wt-main", project_id: projectId, is_main: true },
      { id: "wt-old", project_id: projectId, deleted_at: "2026-01-02T00:00:00.000Z" },
    ]);
    setTransport(mockTransport(server.handlers));

    const live = renderHook(() => useWorktreeList(projectId), { wrapper });
    await waitFor(() => expect(live.result.current.worktrees.map((w) => w.id)).toEqual(["wt-main"]));
    expect(server.calls("ListArchivedWorktrees")).toBe(0);

    const archived = renderHook(() => useArchivedWorktreeList(projectId), { wrapper });
    await waitFor(() => expect(archived.result.current.worktrees.map((w) => w.id)).toEqual(["wt-old"]));
    expect(archived.result.current.hasLoaded).toBe(true);
  });

  it("page on their own token", async () => {
    const server = worktreeServer([
      { id: "wt-main", project_id: projectId, is_main: true },
      ...Array.from({ length: WORKTREE_PAGE_SIZE + 1 }, (_, i) => ({
        id: `wt-old-${i}`,
        project_id: projectId,
        deleted_at: "2026-01-02T00:00:00.000Z",
      })),
    ]);
    setTransport(mockTransport(server.handlers));

    const { result } = renderHook(() => useArchivedWorktreeList(projectId), { wrapper });
    await waitFor(() => expect(result.current.hasMore).toBe(true));
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.worktrees).toHaveLength(WORKTREE_PAGE_SIZE + 1));
    expect(server.calls("ListWorktrees")).toBe(0);
  });
});
