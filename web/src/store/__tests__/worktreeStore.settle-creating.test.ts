import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import type { Worktree as GrpcWorktree } from "../../api/worktree-grpc";

// ---------------------------------------------------------------------------
// CreateWorktree returns BEFORE the workspace exists on disk: the row comes back
// CREATING with an empty path, and the server settles it (ACTIVE + path, or
// FAILED) 30-120s later, announcing that with a `worktree_changes` refetch
// scoped to the worktree id.
//
// The store used to keep the empty-path snapshot forever — nothing listened for
// the settle, and a list refresh left `currentWorktree` pointing at the stale
// object. Every consumer of the path then fell back to the project root, which
// is how a terminal opened "in" a new workspace ran in the main checkout.
// ---------------------------------------------------------------------------

const createMock = vi.hoisted(() => vi.fn());
const listMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/worktree-grpc", () => ({
  worktreeGrpc: {
    create: createMock,
    list: listMock,
  },
}));

vi.mock("../../lib/toast-manager", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { useWorktreeStore } from "../worktreeStore";
import { triggerRefetch } from "../refetchStore";

const projectId = "project-1";
const now = "2026-01-01T00:00:00.000Z";
const settledPath = "/home/u/.reliant/worktrees/proj/triggers-9c37c238";

function grpcWorktree(overrides: Partial<GrpcWorktree>): GrpcWorktree {
  return {
    id: "wt-main",
    name: "Main workspace",
    path: "/home/u/src/proj",
    branch: "main",
    base_branch: "main",
    project_id: projectId,
    status: WorktreeStatus.ACTIVE,
    is_main: true,
    created_at: now,
    updated_at: now,
    last_active: now,
    ...overrides,
  } as GrpcWorktree;
}

const mainRow = grpcWorktree({});
const creatingRow = grpcWorktree({
  id: "wt-new",
  name: "triggers",
  path: "",
  branch: "triggers",
  is_main: false,
  status: WorktreeStatus.CREATING,
});
const settledRow = { ...creatingRow, path: settledPath, status: WorktreeStatus.ACTIVE };

/** Let the refetch debounce fire and the list() promise chain resolve. */
async function flushRefetch() {
  await vi.advanceTimersByTimeAsync(400);
}

describe("worktreeStore: a CREATING worktree settles in place", () => {
  beforeEach(async () => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    useWorktreeStore.getState().reset();
    listMock.mockResolvedValue({ worktrees: [mainRow] });
    await useWorktreeStore.getState().loadWorktrees(projectId);

    createMock.mockResolvedValue(creatingRow);
    await useWorktreeStore.getState().createWorktree({
      project_id: projectId,
      name: "triggers",
      branch: "triggers",
    });
    listMock.mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("starts out pending, with no path", () => {
    const current = useWorktreeStore.getState().currentWorktree;
    expect(current?.id).toBe("wt-new");
    expect(current?.status).toBe(WorktreeStatus.CREATING);
    expect(current?.path).toBe("");
  });

  it("picks up the settled path when the server announces it", async () => {
    listMock.mockResolvedValue({ worktrees: [mainRow, settledRow] });

    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();

    expect(listMock).toHaveBeenCalledTimes(1);
    const state = useWorktreeStore.getState();
    expect(state.worktrees.find((w) => w.id === "wt-new")?.path).toBe(settledPath);
    // The selected snapshot must follow, not just the list — consumers read
    // currentWorktree.path directly.
    expect(state.currentWorktree?.path).toBe(settledPath);
    expect(state.currentWorktree?.status).toBe(WorktreeStatus.ACTIVE);
  });

  it("is not lost when another worktree's refetch lands right after it", async () => {
    // Agent tool calls emit worktree_changes for whatever worktree they ran
    // in, constantly. A per-TYPE debounce kept only the last entity id, so the
    // settle for wt-new was overwritten by noise for wt-main.
    listMock.mockResolvedValue({ worktrees: [mainRow, settledRow] });

    triggerRefetch("worktree_changes", "wt-new");
    await vi.advanceTimersByTimeAsync(50);
    triggerRefetch("worktree_changes", "wt-main");
    await flushRefetch();

    expect(useWorktreeStore.getState().currentWorktree?.path).toBe(settledPath);
  });

  it("does not refetch the list for changes to a worktree that is not pending", async () => {
    // worktree_changes also fires on every agent file edit. Re-listing
    // worktrees for each of those would be pure load.
    triggerRefetch("worktree_changes", "wt-main");
    await flushRefetch();

    expect(listMock).not.toHaveBeenCalled();
  });

  it("records a FAILED settle so callers stop waiting", async () => {
    listMock.mockResolvedValue({
      worktrees: [mainRow, { ...creatingRow, status: WorktreeStatus.FAILED }],
    });

    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();

    expect(useWorktreeStore.getState().currentWorktree?.status).toBe(WorktreeStatus.FAILED);
  });
});
