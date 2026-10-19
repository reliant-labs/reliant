import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import type { Worktree as GrpcWorktree } from "../../api/worktree-grpc";

const listAllMock = vi.hoisted(() => vi.fn());
const getMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/worktree-grpc", () => ({
  worktreeGrpc: { listAll: listAllMock, get: getMock },
}));
vi.mock("../../lib/toast-manager", () => ({
  toast: { success: vi.fn(), error: vi.fn(), loading: vi.fn(), info: vi.fn() },
}));

import { useWorktreeStore } from "../worktreeStore";

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

describe("worktreeStore: more worktrees than one page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useWorktreeStore.getState().reset();
  });

  it("loads every worktree and auto-selects a main worktree that ranks last", async () => {
    const many = Array.from({ length: 112 }, (_, i) => row(`wt-${i}`));
    listAllMock.mockResolvedValue([...many, row("wt-main", { is_main: true })]);

    await useWorktreeStore.getState().loadWorktrees(projectId);

    const state = useWorktreeStore.getState();
    expect(state.worktrees).toHaveLength(113);
    expect(state.currentWorktree?.id).toBe("wt-main");
  });

  it("ensureWorktrees fetches an unloaded worktree by id and merges it", async () => {
    listAllMock.mockResolvedValue([row("wt-main", { is_main: true })]);
    await useWorktreeStore.getState().loadWorktrees(projectId);
    getMock.mockResolvedValue(row("wt-far"));

    await useWorktreeStore.getState().ensureWorktrees(["wt-far", "wt-main", "wt-far"]);

    expect(getMock).toHaveBeenCalledTimes(1);
    expect(getMock).toHaveBeenCalledWith("wt-far");
    expect(useWorktreeStore.getState().worktrees.map((w) => w.id)).toEqual(["wt-main", "wt-far"]);
  });

  it("resolveWorktree answers from the store without a request when it can", async () => {
    listAllMock.mockResolvedValue([row("wt-main", { is_main: true })]);
    await useWorktreeStore.getState().loadWorktrees(projectId);

    expect((await useWorktreeStore.getState().resolveWorktree("wt-main"))?.id).toBe("wt-main");
    expect(getMock).not.toHaveBeenCalled();
  });

  it("does not ask again for a worktree the server says is gone", async () => {
    getMock.mockRejectedValue(new ConnectError("nope", Code.NotFound));

    expect(await useWorktreeStore.getState().resolveWorktree("wt-gone")).toBeNull();
    await useWorktreeStore.getState().ensureWorktrees(["wt-gone"]);

    expect(getMock).toHaveBeenCalledTimes(1);
  });

  it("retries after a transient failure", async () => {
    getMock.mockRejectedValueOnce(new ConnectError("down", Code.Unavailable));
    getMock.mockResolvedValueOnce(row("wt-far"));

    await useWorktreeStore.getState().ensureWorktrees(["wt-far"]);
    expect(useWorktreeStore.getState().worktrees).toHaveLength(0);
    await useWorktreeStore.getState().ensureWorktrees(["wt-far"]);

    expect(useWorktreeStore.getState().worktrees.map((w) => w.id)).toEqual(["wt-far"]);
  });
});
