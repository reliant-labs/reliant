import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import type { Worktree } from "../worktreeStore";

const createMock = vi.hoisted(() => vi.fn());
const listMock = vi.hoisted(() => vi.fn());
const toastSuccessMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/worktree-grpc", () => ({
  worktreeGrpc: {
    create: createMock,
    list: listMock,
  },
}));

vi.mock("../../lib/toast-manager", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

import { useWorktreeStore } from "../worktreeStore";

const projectId = "project-1";
const now = "2026-01-01T00:00:00.000Z";

function buildWorktree(overrides: Partial<Worktree>): Worktree {
  return {
    id: "wt-main",
    name: "Main workspace",
    path: "/tmp/project",
    branch: "main",
    base_branch: "main",
    project_id: projectId,
    status: WorktreeStatus.UNSPECIFIED,
    is_main: true,
    created_at: now,
    updated_at: now,
    last_active: now,
    deleted_at: null,
    ...overrides,
  };
}

describe("worktreeStore.createWorktree in a multi-repo project", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useWorktreeStore.getState().reset();
    useWorktreeStore.setState({
      worktrees: [buildWorktree({})],
      hasLoaded: true,
    });

    createMock.mockResolvedValue({
      id: "wt-new",
      name: "new-feature",
      path: "/tmp/worktrees/wt-new",
      branch: "new-feature",
      base_branch: "main",
      project_id: projectId,
      status: WorktreeStatus.ACTIVE,
      is_main: false,
      created_at: now,
      updated_at: now,
      last_active: now,
    });
  });

  it("appends the new worktree to the store and selects it", async () => {
    const created = await useWorktreeStore.getState().createWorktree({
      project_id: projectId,
      name: "new-feature",
      branch: "new-feature",
      base_branches: { "repo-a": "main", "repo-b": "develop" },
    });

    expect(created.id).toBe("wt-new");
    expect(useWorktreeStore.getState().worktrees.map((w) => w.id)).toContain("wt-new");
    expect(useWorktreeStore.getState().currentWorktree?.id).toBe("wt-new");
  });

  it("forwards per-repo base branch overrides to the RPC layer", async () => {
    await useWorktreeStore.getState().createWorktree({
      project_id: projectId,
      name: "new-feature",
      branch: "new-feature",
      base_branches: { "repo-a": "main", "repo-b": "develop" },
    });

    expect(createMock).toHaveBeenCalledWith(
      projectId,
      "new-feature",
      "new-feature",
      expect.objectContaining({
        baseBranches: { "repo-a": "main", "repo-b": "develop" },
      })
    );
  });
});

describe("worktreeStore.refreshWorktrees", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useWorktreeStore.getState().reset();
    useWorktreeStore.setState({
      worktrees: [buildWorktree({})],
      hasLoaded: true,
      lastLoadIncludedArchived: false,
    });
  });

  it("refetches even when an identical load is already in flight", async () => {
    // First call resolves with pre-mutation data, second with post-mutation
    // data. A singleflight-joined refresh would observe only the first.
    listMock
      .mockResolvedValueOnce({
        worktrees: [
          {
            id: "wt-main",
            name: "Main workspace",
            path: "/tmp/project",
            branch: "main",
            base_branch: "main",
            project_id: projectId,
            status: WorktreeStatus.UNSPECIFIED,
            is_main: true,
            created_at: now,
            updated_at: now,
            last_active: now,
          },
        ],
      })
      .mockResolvedValueOnce({
        worktrees: [
          {
            id: "wt-main",
            name: "Main workspace",
            path: "/tmp/project",
            branch: "main",
            base_branch: "main",
            project_id: projectId,
            status: WorktreeStatus.UNSPECIFIED,
            is_main: true,
            created_at: now,
            updated_at: now,
            last_active: now,
          },
          {
            id: "wt-restored",
            name: "Restored workspace",
            path: "/tmp/worktrees/wt-restored",
            branch: "restored",
            base_branch: "main",
            project_id: projectId,
            status: WorktreeStatus.ACTIVE,
            is_main: false,
            created_at: now,
            updated_at: now,
            last_active: now,
          },
        ],
      });

    const inFlight = useWorktreeStore.getState().loadWorktrees(projectId);
    const refresh = useWorktreeStore.getState().refreshWorktrees(projectId);

    await Promise.all([inFlight, refresh]);

    expect(listMock).toHaveBeenCalledTimes(2);
    expect(useWorktreeStore.getState().worktrees.map((w) => w.id)).toContain(
      "wt-restored"
    );
  });
});
