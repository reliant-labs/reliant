/**
 * WorktreeService reads and writes share the generated key scope: a lifecycle
 * write refetches the mounted workspace reads (so a picker, the Workspaces
 * page and the selection never show a stale row), and the stream's
 * `worktree_changes` invalidation reaches every workspace list and the reads
 * that name its workspace — and not another workspace's diffs, each of which
 * is a round trip to a machine.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WorktreeStatus } from "@/gen/reliant/v1/worktree_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { triggerRefetch } from "@/store/refetchStore";
import { useWorktreeStore } from "@/store/worktreeStore";
import { selectWorktreeForTest } from "@/test/worktreeCache";
import { worktreeServer } from "@/test/worktreeServer";
import { createWorktree } from "../worktree-actions";
import { useWorkspaceGitStatus } from "../worktree-git-queries";
import {
  invalidateWorktreeQueries,
  useCurrentWorktree,
  useMainWorktree,
  useWorktreeById,
  useWorktreeList,
} from "../worktree-queries";
import { worktreeServiceKeys } from "../worktree-service-hooks_gen";

vi.mock("@/lib/toast-manager", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), loading: vi.fn(), warning: vi.fn() },
}));

const projectId = "project-1";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

function gitStatusHandlers(counts: Record<string, number>) {
  return {
    "WorktreeService.GetWorktreeGitStatus": (req: unknown) => {
      const id = (req as { worktreeId: string }).worktreeId;
      counts[id] = (counts[id] ?? 0) + 1;
      return { branch: `branch-${id}`, clean: true };
    },
  };
}

beforeEach(() => {
  useWorktreeStore.getState().reset();
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
  useWorktreeStore.getState().reset();
});

describe("WorktreeService keys", () => {
  it("creating a workspace refetches the mounted live list and selects the new row", async () => {
    const server = worktreeServer([{ id: "wt-main", project_id: projectId, is_main: true, name: "main" }]);
    setTransport(mockTransport(server.handlers));

    const list = renderHook(() => useWorktreeList(projectId), { wrapper });
    const current = renderHook(() => useCurrentWorktree(), { wrapper });
    await waitFor(() => expect(list.result.current.worktrees.map((w) => w.id)).toEqual(["wt-main"]));

    const created = await createWorktree({ project_id: projectId, name: "feature", branch: "feature" });

    await waitFor(() => expect(list.result.current.worktrees.map((w) => w.id)).toEqual(["wt-main", created.id]));
    expect(server.calls("ListWorktrees")).toBe(2);
    expect(current.result.current?.id).toBe(created.id);
    expect(current.result.current?.status).toBe(WorktreeStatus.CREATING);
  });

  it("a worktree_changes event for a known workspace refetches its reads, marks the lists stale, and leaves other workspaces alone", async () => {
    const server = worktreeServer([
      { id: "wt-main", project_id: projectId, is_main: true },
      { id: "wt-a", project_id: projectId },
      { id: "wt-b", project_id: projectId },
    ]);
    const gitStatusCalls: Record<string, number> = {};
    setTransport(mockTransport({ ...server.handlers, ...gitStatusHandlers(gitStatusCalls) }));

    const list = renderHook(() => useWorktreeList(projectId), { wrapper });
    const a = renderHook(() => useWorkspaceGitStatus("wt-a"), { wrapper });
    const b = renderHook(() => useWorkspaceGitStatus("wt-b"), { wrapper });
    const row = renderHook(() => useWorktreeById("wt-a"), { wrapper });
    await waitFor(() => {
      expect(list.result.current.worktrees).toHaveLength(3);
      expect(a.result.current.isSuccess && b.result.current.isSuccess).toBe(true);
      expect(row.result.current?.id).toBe("wt-a");
    });
    const listCalls = server.calls("ListWorktrees");
    const rowCalls = server.calls("GetWorktree");

    // The event fires on every file-mutating tool call: churn, for a
    // workspace the lists already hold.
    act(() => triggerRefetch("worktree_changes", "wt-a"));

    await waitFor(() => {
      expect(gitStatusCalls["wt-a"]).toBe(2);
      expect(server.calls("GetWorktree")).toBe(rowCalls + 1);
    });
    expect(gitStatusCalls["wt-b"]).toBe(1);
    expect(server.calls("ListWorktrees")).toBe(listCalls);
    const listKey = worktreeServiceKeys.listWorktreesInfinite({ projectId, limit: 100 });
    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(true);
  });

  it("a worktree_changes event for a workspace no list holds yet refetches the mounted lists", async () => {
    const server = worktreeServer([{ id: "wt-main", project_id: projectId, is_main: true }]);
    setTransport(mockTransport(server.handlers));
    const list = renderHook(() => useWorktreeList(projectId), { wrapper });
    const main = renderHook(() => useMainWorktree(projectId), { wrapper });
    await waitFor(() => {
      expect(list.result.current.worktrees).toHaveLength(1);
      expect(main.result.current?.id).toBe("wt-main");
    });

    // An agent made a workspace (the worktree tool emits for the new id).
    server.rows.push({ ...server.rows[0], id: "wt-agent", is_main: false, name: "agent" });
    act(() => triggerRefetch("worktree_changes", "wt-agent"));

    await waitFor(() => expect(list.result.current.worktrees.map((w) => w.id)).toEqual(["wt-main", "wt-agent"]));
  });

  it("service-wide invalidation reaches every read under worktreeServiceKeys.all", async () => {
    const server = worktreeServer([
      { id: "wt-main", project_id: projectId, is_main: true },
      { id: "wt-a", project_id: projectId },
    ]);
    const gitStatusCalls: Record<string, number> = {};
    setTransport(mockTransport({ ...server.handlers, ...gitStatusHandlers(gitStatusCalls) }));

    const byId = renderHook(() => useWorktreeById("wt-a"), { wrapper });
    const status = renderHook(() => useWorkspaceGitStatus("wt-a"), { wrapper });
    await waitFor(() => {
      expect(byId.result.current?.id).toBe("wt-a");
      expect(status.result.current.isSuccess).toBe(true);
    });
    expect(queryClient.getQueryData(worktreeServiceKeys.getWorktree({ worktreeId: "wt-a" }))).toBeDefined();

    await invalidateWorktreeQueries(queryClient);

    expect(server.calls("GetWorktree")).toBe(2);
    expect(gitStatusCalls["wt-a"]).toBe(2);
  });

  it("the selection always shows the newest row: a settle refetch fills in a CREATING workspace's path", async () => {
    const server = worktreeServer([{ id: "wt-main", project_id: projectId, is_main: true }]);
    setTransport(mockTransport(server.handlers));
    const current = renderHook(() => useCurrentWorktree(), { wrapper });

    const created = await createWorktree({ project_id: projectId, name: "feature", branch: "feature" });
    await waitFor(() => expect(current.result.current?.path).toBe(""));

    server.update(created.id, { status: WorktreeStatus.ACTIVE, path: "/tmp/feature" });
    act(() => triggerRefetch("worktree_changes", created.id));

    await waitFor(() => expect(current.result.current?.path).toBe("/tmp/feature"));
    expect(current.result.current?.status).toBe(WorktreeStatus.ACTIVE);
  });

  it("a workspace selected from a list is read from that list without a GetWorktree", async () => {
    const server = worktreeServer([
      { id: "wt-main", project_id: projectId, is_main: true },
      { id: "wt-a", project_id: projectId, name: "a" },
    ]);
    setTransport(mockTransport(server.handlers));
    const list = renderHook(() => useWorktreeList(projectId), { wrapper });
    await waitFor(() => expect(list.result.current.worktrees).toHaveLength(2));

    act(() => useWorktreeStore.getState().selectWorktree({ id: "wt-a", project_id: projectId }));
    const current = renderHook(() => useCurrentWorktree(), { wrapper });

    expect(current.result.current?.name).toBe("a");
    expect(server.calls("GetWorktree")).toBe(0);
  });

  it("selectWorktreeForTest seeds a row every reader resolves", () => {
    selectWorktreeForTest({ id: "wt-seeded", name: "seeded", project_id: projectId });
    const current = renderHook(() => useCurrentWorktree(), { wrapper });
    expect(current.result.current?.name).toBe("seeded");
  });
});
