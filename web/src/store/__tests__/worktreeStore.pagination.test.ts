import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import type { Worktree as GrpcWorktree } from "../../api/worktree-grpc";

const listMock = vi.hoisted(() => vi.fn());
const getMainMock = vi.hoisted(() => vi.fn());
const listArchivedMock = vi.hoisted(() => vi.fn());
const getMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/worktree-grpc", () => ({
  worktreeGrpc: { list: listMock, getMain: getMainMock, listArchived: listArchivedMock, get: getMock },
}));
vi.mock("../../lib/toast-manager", () => ({
  toast: { success: vi.fn(), error: vi.fn(), loading: vi.fn(), info: vi.fn() },
}));

import { useWorktreeStore } from "../worktreeStore";
import { useWorkspaceStateStore } from "../workspaceStateStore";

const projectId = "project-1";
const now = "2026-01-01T00:00:00.000Z";

function row(id: string, overrides: Partial<GrpcWorktree> = {}): GrpcWorktree {
  return {
    id,
    name: id,
    path: `/w/${id}`,
    branch: id,
    base_branch: "main",
    project_id: projectId,
    status: WorktreeStatus.ACTIVE,
    is_main: false,
    created_at: now,
    updated_at: now,
    last_active: now,
    ...overrides,
  } as GrpcWorktree;
}

const page = (worktrees: GrpcWorktree[], nextPageToken = "") => ({
  worktrees,
  total: worktrees.length,
  nextPageToken,
});

describe("worktreeStore: the full list is on-demand and paged", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useWorktreeStore.getState().reset();
    useWorkspaceStateStore.getState().setLastWorktree(projectId, null);
  });

  describe("main workspace without the list", () => {
    it("loadMainWorktree fetches one row and selects it, never listing the project", async () => {
      getMainMock.mockResolvedValue(row("wt-main", { is_main: true }));

      await useWorktreeStore.getState().loadMainWorktree(projectId);

      const state = useWorktreeStore.getState();
      expect(getMainMock).toHaveBeenCalledWith(projectId);
      expect(listMock).not.toHaveBeenCalled();
      expect(state.worktrees.map((w) => w.id)).toEqual(["wt-main"]);
      expect(state.currentWorktree?.id).toBe("wt-main");
    });

    it("restoreLastWorktree selects main by fetching it alone when nothing is saved", async () => {
      getMainMock.mockResolvedValue(row("wt-main", { is_main: true }));

      expect(await useWorktreeStore.getState().restoreLastWorktree(projectId)).toBe(true);

      expect(listMock).not.toHaveBeenCalled();
      expect(useWorktreeStore.getState().currentWorktree?.id).toBe("wt-main");
    });

    it("restoreLastWorktree fetches the saved workspace by id, wherever it sits in the project", async () => {
      useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-far");
      getMock.mockResolvedValue(row("wt-far"));

      expect(await useWorktreeStore.getState().restoreLastWorktree(projectId)).toBe(true);

      expect(getMock).toHaveBeenCalledWith("wt-far");
      expect(listMock).not.toHaveBeenCalled();
      expect(useWorktreeStore.getState().currentWorktree?.id).toBe("wt-far");
    });

    it("restoreLastWorktree falls back to main when the saved workspace was archived or deleted", async () => {
      useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-old");
      getMock.mockResolvedValue(row("wt-old", { deleted_at: now }));
      getMainMock.mockResolvedValue(row("wt-main", { is_main: true }));

      await useWorktreeStore.getState().restoreLastWorktree(projectId);

      expect(useWorktreeStore.getState().currentWorktree?.id).toBe("wt-main");
      expect(useWorkspaceStateStore.getState().getProjectState(projectId).lastWorktreeId).toBeNull();
    });
  });

  it("restoreLastWorktree keeps the saved workspace when the lookup merely failed", async () => {
    useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-far");
    getMock.mockRejectedValue(new ConnectError("offline", Code.Unavailable));
    getMainMock.mockResolvedValue(row("wt-main", { is_main: true }));

    await useWorktreeStore.getState().restoreLastWorktree(projectId);

    expect(useWorktreeStore.getState().currentWorktree?.id).toBe("wt-main");
    expect(useWorkspaceStateStore.getState().getProjectState(projectId).lastWorktreeId).toBe("wt-far");
  });

  it("restoreLastWorktree forgets a saved workspace the server says is gone", async () => {
    useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-gone");
    getMock.mockRejectedValue(new ConnectError("nope", Code.NotFound));
    getMainMock.mockResolvedValue(row("wt-main", { is_main: true }));

    await useWorktreeStore.getState().restoreLastWorktree(projectId);

    expect(useWorkspaceStateStore.getState().getProjectState(projectId).lastWorktreeId).toBeNull();
  });

  describe("the live list", () => {
    it("loads one page and keeps the token for the next", async () => {
      listMock.mockResolvedValue(page([row("wt-main", { is_main: true }), row("a")], "t2"));

      await useWorktreeStore.getState().loadWorktrees(projectId);

      const state = useWorktreeStore.getState();
      expect(listMock).toHaveBeenCalledTimes(1);
      expect(state.worktrees).toHaveLength(2);
      expect(state.nextPageToken).toBe("t2");
      expect(state.currentWorktree?.id).toBe("wt-main");
    });

    it("loadMoreWorktrees appends the next page and ends at the last", async () => {
      listMock.mockResolvedValueOnce(page([row("wt-main", { is_main: true }), row("a")], "t2"));
      await useWorktreeStore.getState().loadWorktrees(projectId);
      listMock.mockResolvedValueOnce(page([row("b"), row("c")], ""));

      await useWorktreeStore.getState().loadMoreWorktrees(projectId);

      const state = useWorktreeStore.getState();
      expect(listMock.mock.calls[1]![1]).toMatchObject({ pageToken: "t2" });
      expect(state.worktrees.map((w) => w.id)).toEqual(["wt-main", "a", "b", "c"]);
      expect(state.nextPageToken).toBe("");

      await useWorktreeStore.getState().loadMoreWorktrees(projectId);
      expect(listMock).toHaveBeenCalledTimes(2);
    });

    it("drops a load-more that was in flight when the list was reloaded", async () => {
      listMock.mockResolvedValueOnce(page([row("wt-main", { is_main: true })], "t2"));
      await useWorktreeStore.getState().loadWorktrees(projectId);

      let finish!: (v: ReturnType<typeof page>) => void;
      listMock.mockReturnValueOnce(new Promise((resolve) => (finish = resolve)));
      const more = useWorktreeStore.getState().loadMoreWorktrees(projectId);

      listMock.mockResolvedValueOnce(page([row("wt-main", { is_main: true }), row("fresh")], ""));
      await useWorktreeStore.getState().refreshWorktrees(projectId);
      finish(page([row("stale")], "t3"));
      await more;

      const state = useWorktreeStore.getState();
      expect(state.worktrees.map((w) => w.id)).toEqual(["wt-main", "fresh"]);
      expect(state.nextPageToken).toBe("");
    });

    it("keeps the selected workspace when it lies beyond the first page", async () => {
      useWorktreeStore.setState({ currentWorktree: { ...row("wt-far"), deleted_at: null } as never });
      listMock.mockResolvedValue(page([row("wt-main", { is_main: true })], "t2"));

      await useWorktreeStore.getState().loadWorktrees(projectId);

      expect(useWorktreeStore.getState().currentWorktree?.id).toBe("wt-far");
    });
  });

  describe("archived workspaces are a separate query", () => {
    it("never enter the live list, and load only when asked", async () => {
      listMock.mockResolvedValue(page([row("wt-main", { is_main: true })]));
      await useWorktreeStore.getState().loadWorktrees(projectId);
      expect(listArchivedMock).not.toHaveBeenCalled();

      listArchivedMock.mockResolvedValue(page([row("old", { deleted_at: now })], "a2"));
      await useWorktreeStore.getState().loadArchivedWorktrees(projectId);

      const state = useWorktreeStore.getState();
      expect(state.worktrees.map((w) => w.id)).toEqual(["wt-main"]);
      expect(state.archivedWorktrees.map((w) => w.id)).toEqual(["old"]);
      expect(state.archivedNextPageToken).toBe("a2");
    });

    it("page on their own token", async () => {
      listArchivedMock.mockResolvedValueOnce(page([row("o1", { deleted_at: now })], "a2"));
      await useWorktreeStore.getState().loadArchivedWorktrees(projectId);
      listArchivedMock.mockResolvedValueOnce(page([row("o2", { deleted_at: now })], ""));

      await useWorktreeStore.getState().loadMoreArchivedWorktrees(projectId);

      expect(listArchivedMock.mock.calls[1]![1]).toMatchObject({ pageToken: "a2" });
      expect(useWorktreeStore.getState().archivedWorktrees.map((w) => w.id)).toEqual(["o1", "o2"]);
    });
  });

  describe("resolving one workspace by id", () => {
    it("answers from the store without a request when it can", async () => {
      getMainMock.mockResolvedValue(row("wt-main", { is_main: true }));
      await useWorktreeStore.getState().loadMainWorktree(projectId);

      expect((await useWorktreeStore.getState().resolveWorktree("wt-main"))?.id).toBe("wt-main");
      expect(getMock).not.toHaveBeenCalled();
    });

    it("fetches an unloaded workspace once even for concurrent callers", async () => {
      getMock.mockResolvedValue(row("wt-far"));

      const [a, b] = await Promise.all([
        useWorktreeStore.getState().resolveWorktree("wt-far"),
        useWorktreeStore.getState().resolveWorktree("wt-far"),
      ]);

      expect(getMock).toHaveBeenCalledTimes(1);
      expect(a?.id).toBe("wt-far");
      expect(b?.id).toBe("wt-far");
      expect(useWorktreeStore.getState().worktrees.map((w) => w.id)).toEqual(["wt-far"]);
    });

    it("does not ask again for a worktree the server says is gone", async () => {
      getMock.mockRejectedValue(new ConnectError("nope", Code.NotFound));

      expect(await useWorktreeStore.getState().resolveWorktree("wt-gone")).toBeNull();
      expect(await useWorktreeStore.getState().resolveWorktree("wt-gone")).toBeNull();

      expect(getMock).toHaveBeenCalledTimes(1);
    });

    it("retries after a transient failure", async () => {
      getMock.mockRejectedValueOnce(new ConnectError("down", Code.Unavailable));
      getMock.mockResolvedValueOnce(row("wt-far"));

      expect(await useWorktreeStore.getState().resolveWorktree("wt-far")).toBeNull();
      expect((await useWorktreeStore.getState().resolveWorktree("wt-far"))?.id).toBe("wt-far");
    });
  });
});
