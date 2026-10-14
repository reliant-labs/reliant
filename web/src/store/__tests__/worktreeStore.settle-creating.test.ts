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
  toast: { success: vi.fn(), error: vi.fn(), loading: vi.fn() },
}));

import { useWorktreeStore } from "../worktreeStore";
import { triggerRefetch } from "../refetchStore";
import { toast } from "../../lib/toast-manager";

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

  // Prod, 2026-10-13: a create named a base branch that did not exist. The
  // server knew exactly what to tell the user; the UI showed only a "Failed"
  // badge, after a "created successfully" toast.
  it("says why a FAILED settle failed, once", async () => {
    const reason =
      "base branch 'feat/telemetry-delivery-c2' doesn't exist in reliant — check the name, or start from an existing branch";
    listMock.mockResolvedValue({
      worktrees: [mainRow, { ...creatingRow, status: WorktreeStatus.FAILED, failure_reason: reason }],
    });

    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();

    expect(useWorktreeStore.getState().currentWorktree?.failure_reason).toBe(reason);
    expect(toast.error).toHaveBeenCalledTimes(1);
    expect(vi.mocked(toast.error).mock.calls[0][0]).toBe(`Couldn't create workspace "triggers": ${reason}`);

    // A later refetch for the same row is not a second failure.
    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();
    expect(toast.error).toHaveBeenCalledTimes(1);
  });

  it("still says a FAILED settle failed when the reason is ours", async () => {
    listMock.mockResolvedValue({
      worktrees: [mainRow, { ...creatingRow, status: WorktreeStatus.FAILED }],
    });

    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();

    expect(toast.error).toHaveBeenCalledTimes(1);
    expect(vi.mocked(toast.error).mock.calls[0][0]).toContain("did not finish");
  });

  it("says nothing when the create succeeds", async () => {
    listMock.mockResolvedValue({ worktrees: [mainRow, settledRow] });

    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();

    expect(toast.error).not.toHaveBeenCalled();
  });

  // The create settles 30-120s later and can fail, so "created successfully"
  // at accept time was followed by a failure toast. One toast per create,
  // replaced in place under a shared id.
  it("shows a pending toast when the create is accepted, not success", () => {
    expect(toast.loading).toHaveBeenCalledTimes(1);
    const [message, options] = vi.mocked(toast.loading).mock.calls[0];
    expect(message).toBe(`Creating workspace "triggers"…`);
    expect(options?.id).toBeTruthy();
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("shows success, replacing the pending toast, once the worktree is ACTIVE", async () => {
    listMock.mockResolvedValue({ worktrees: [mainRow, settledRow] });

    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();

    expect(toast.success).toHaveBeenCalledTimes(1);
    const [message, options] = vi.mocked(toast.success).mock.calls[0];
    expect(message).toBe(`Workspace "triggers" created successfully`);
    expect(options?.id).toBe(vi.mocked(toast.loading).mock.calls[0][1]?.id);

    // A later refetch for the same row is not a second success.
    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();
    expect(toast.success).toHaveBeenCalledTimes(1);
  });

  it("shows only the failure toast, replacing the pending one, when it FAILS", async () => {
    listMock.mockResolvedValue({
      worktrees: [mainRow, { ...creatingRow, status: WorktreeStatus.FAILED, failure_reason: "bad base" }],
    });

    triggerRefetch("worktree_changes", "wt-new");
    await flushRefetch();

    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledTimes(1);
    const options = vi.mocked(toast.error).mock.calls[0][1];
    expect(options?.id).toBe(vi.mocked(toast.loading).mock.calls[0][1]?.id);
  });
});
