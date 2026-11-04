/**
 * The workspace lifecycle through the real transport seam: create, settle,
 * archive, restore (unarchive / recreate), permanent delete, and selecting a
 * project's workspace at start-up. Every row lives in the query cache, so each
 * test asserts what a reader would see there (getCachedWorktree,
 * getCurrentWorktree) rather than a store field.
 *
 * Branched chats depend on this path (Chat → Worktree → active daemon), so
 * each step is pinned on its own.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WorktreeStatus } from "@/gen/reliant/v1/worktree_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { toast } from "@/lib/toast-manager";
import { useChatNavigationStore } from "@/store/chatNavigationStore";
import { useChatStore } from "@/store/chatStore";
import { triggerRefetch } from "@/store/refetchStore";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";
import { useWorktreeStore } from "@/store/worktreeStore";
import { seedWorktreeList } from "@/test/worktreeCache";
import { worktreeServer, type WorktreeServer } from "@/test/worktreeServer";
import {
  archiveWorktree,
  createWorktree,
  deleteWorktree,
  loadMainWorktree,
  restoreLastWorktree,
  unarchiveWorktree,
  updateWorktreeStatus,
} from "../worktree-actions";
import {
  getCachedWorktree,
  getCachedWorktrees,
  getCurrentWorktree,
  isWorktreeKnownGone,
  resetWorktreeAnnouncements,
  resolveWorktree,
} from "../worktree-queries";
import { worktreeServiceKeys } from "../worktree-service-hooks_gen";

vi.mock("@/lib/toast-manager", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), loading: vi.fn(), warning: vi.fn() },
}));

vi.mock("@/store/browserStore", () => ({
  useBrowserStore: { getState: () => ({ closeWorktreeTabs: vi.fn() }) },
}));

const projectId = "project-1";
const mainRow = { id: "wt-main", name: "Main workspace", path: "/tmp/project", project_id: projectId, is_main: true };
const featureRow = { id: "wt-feature", name: "Feature", path: "/tmp/feature", project_id: projectId };

let server: WorktreeServer;

function serve(rows: Parameters<typeof worktreeServer>[0], options?: Parameters<typeof worktreeServer>[1]) {
  server = worktreeServer(rows, options);
  setTransport(mockTransport(server.handlers));
  return server;
}

/** Let refetchStore's 300ms debounce fire and the refetch it starts settle. */
async function flushRefetch() {
  await vi.advanceTimersByTimeAsync(400);
  await vi.waitFor(() => expect(queryClient.isFetching()).toBe(0));
}

beforeEach(() => {
  vi.clearAllMocks();
  // Archive and restore reload the chat list; that is the chat lane's RPC.
  useChatStore.setState({ loadChats: vi.fn().mockResolvedValue(undefined) });
  useWorktreeStore.getState().reset();
  useWorkspaceStateStore.getState().reset();
  resetWorktreeAnnouncements();
});

afterEach(() => {
  vi.useRealTimers();
  queryClient.clear();
  setTransport(null);
});

describe("create", () => {
  it("caches the new row, selects it, and announces it pending", async () => {
    serve([mainRow]);
    const created = await createWorktree({ project_id: projectId, name: "triggers", branch: "triggers" });

    expect(created.status).toBe(WorktreeStatus.CREATING);
    expect(getCurrentWorktree()?.id).toBe(created.id);
    expect(getCachedWorktree(created.id)?.path).toBe("");
    expect(useWorkspaceStateStore.getState().getProjectState(projectId).lastWorktreeId).toBe(created.id);
    expect(toast.loading).toHaveBeenCalledWith(`Creating workspace "triggers"…`, expect.objectContaining({ id: expect.any(String) }));
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("forwards per-repo base branch overrides and copy paths", async () => {
    serve([mainRow]);
    await createWorktree({
      project_id: projectId,
      name: "multi",
      branch: "multi",
      base_branch: "main",
      base_branches: { "repo-api": "develop", "repo-web": "release" },
      copy_files: [".env", "web/node_modules"],
    });

    const [request] = server.requests("CreateWorktree");
    expect(request.baseBranches).toEqual({ "repo-api": "develop", "repo-web": "release" });
    expect(request.baseBranch).toBe("main");
    expect(request.copyFiles).toEqual([".env", "web/node_modules"]);
  });

  it("rejects without the required fields and calls nothing", async () => {
    serve([mainRow]);
    await expect(createWorktree({ project_id: projectId, name: "" })).rejects.toThrow(/required/);
    expect(server.calls("CreateWorktree")).toBe(0);
  });

  it("announces a create that comes back ACTIVE as created, with no pending toast", async () => {
    serve([mainRow], { createStatus: WorktreeStatus.ACTIVE });
    await createWorktree({ project_id: projectId, name: "fast", branch: "fast" });
    expect(toast.loading).not.toHaveBeenCalled();
    expect(toast.success).toHaveBeenCalledWith(`Workspace "fast" created successfully`, expect.anything());
  });
});

describe("a CREATING workspace settles in place", () => {
  async function created() {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    serve([mainRow]);
    return createWorktree({ project_id: projectId, name: "triggers", branch: "triggers" });
  }

  it("picks up the settled path when the server announces it, replacing the pending toast with success once", async () => {
    const row = await created();
    server.update(row.id, { status: WorktreeStatus.ACTIVE, path: "/tmp/settled" });

    triggerRefetch("worktree_changes", row.id);
    await flushRefetch();

    expect(getCurrentWorktree()?.path).toBe("/tmp/settled");
    expect(toast.success).toHaveBeenCalledTimes(1);
    const [message, options] = vi.mocked(toast.success).mock.calls[0];
    expect(message).toBe(`Workspace "triggers" created successfully`);
    expect(options?.id).toBe(vi.mocked(toast.loading).mock.calls[0][1]?.id);

    triggerRefetch("worktree_changes", row.id);
    await flushRefetch();
    expect(toast.success).toHaveBeenCalledTimes(1);
  });

  it("is not lost when another workspace's event lands right after it", async () => {
    const row = await created();
    server.update(row.id, { status: WorktreeStatus.ACTIVE, path: "/tmp/settled" });

    triggerRefetch("worktree_changes", row.id);
    triggerRefetch("worktree_changes", "wt-main");
    await flushRefetch();

    expect(getCachedWorktree(row.id)?.path).toBe("/tmp/settled");
  });

  it("says why a FAILED settle failed, once, replacing the pending toast", async () => {
    const row = await created();
    const reason = "base branch 'nope' doesn't exist in reliant — check the name";
    server.update(row.id, { status: WorktreeStatus.FAILED, failure_reason: reason });

    triggerRefetch("worktree_changes", row.id);
    await flushRefetch();

    expect(getCurrentWorktree()?.status).toBe(WorktreeStatus.FAILED);
    expect(getCurrentWorktree()?.failure_reason).toBe(reason);
    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledTimes(1);
    expect(vi.mocked(toast.error).mock.calls[0][0]).toBe(`Couldn't create workspace "triggers": ${reason}`);
    expect(vi.mocked(toast.error).mock.calls[0][1]?.id).toBe(vi.mocked(toast.loading).mock.calls[0][1]?.id);

    triggerRefetch("worktree_changes", row.id);
    await flushRefetch();
    expect(toast.error).toHaveBeenCalledTimes(1);
  });

  it("still says a FAILED settle failed when the reason is ours", async () => {
    const row = await created();
    server.update(row.id, { status: WorktreeStatus.FAILED });

    triggerRefetch("worktree_changes", row.id);
    await flushRefetch();

    expect(vi.mocked(toast.error).mock.calls[0][0]).toContain("did not finish");
  });

  it("re-reads nothing by id for a workspace that is not pending", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    serve([mainRow, featureRow]);
    await resolveWorktree("wt-feature");
    const reads = server.calls("GetWorktree");

    // Nothing mounts the by-id read, so only a pending create re-reads it.
    triggerRefetch("worktree_changes", "wt-feature");
    await flushRefetch();

    expect(server.calls("GetWorktree")).toBe(reads);
    expect(toast.success).not.toHaveBeenCalled();
  });
});

describe("archive", () => {
  it("archives the selected workspace, drops it from the live list, and switches to main with a fresh chat", async () => {
    serve([mainRow, featureRow]);
    seedWorktreeList(projectId, [mainRow, featureRow]);
    useWorktreeStore.getState().selectWorktree({ id: "wt-feature", project_id: projectId });
    const switchSpy = vi.spyOn(useWorktreeStore.getState(), "switchWorktreeContext");

    await archiveWorktree("wt-feature");

    expect(server.calls("ArchiveWorktree")).toBe(1);
    expect(getCachedWorktree("wt-feature")?.deleted_at).toBeTruthy();
    expect(getCachedWorktrees(projectId).filter((w) => !w.deleted_at).map((w) => w.id)).toEqual(["wt-main"]);
    expect(switchSpy).toHaveBeenCalledWith(projectId, expect.objectContaining({ id: "wt-main" }), { openFreshNewChat: true });
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("wt-main");
    expect(useWorktreeStore.getState().deletingId).toBeNull();
    expect(toast.success).toHaveBeenCalledWith("Feature archived", expect.anything());
  });

  it("clears the active chat that lived in the archived workspace and drops it from the queue", async () => {
    serve([mainRow, featureRow]);
    seedWorktreeList(projectId, [mainRow, featureRow]);
    useWorktreeStore.getState().selectWorktree({ id: "wt-feature", project_id: projectId });
    const clearCurrentChat = vi.fn(() => useChatStore.setState({ activeChatId: null }));
    useChatStore.setState({ activeChatId: "chat-feature", clearCurrentChat });
    useChatNavigationStore.setState({ chatQueue: ["chat-feature", "chat-main"] });

    await archiveWorktree("wt-feature");

    expect(clearCurrentChat).toHaveBeenCalledWith("wt-feature");
    expect(useChatStore.getState().activeChatId).toBeNull();
    expect(useChatNavigationStore.getState().chatQueue).not.toContain("chat-feature");
  });

  it("says when the branch goes too", async () => {
    serve([mainRow, featureRow]);
    seedWorktreeList(projectId, [mainRow, featureRow]);
    await archiveWorktree("wt-feature", { deleteGitBranch: true });
    expect(server.requests("ArchiveWorktree")[0].deleteGitBranch).toBe(true);
    expect(toast.success).toHaveBeenCalledWith(
      "Feature archived (branch will be deleted once its files are removed)",
      expect.anything(),
    );
  });

  it("toasts a failure and leaves the selection alone", async () => {
    serve([mainRow, featureRow]);
    seedWorktreeList(projectId, [mainRow, featureRow]);
    useWorktreeStore.getState().selectWorktree({ id: "wt-feature", project_id: projectId });
    server.fail("ArchiveWorktree", new ConnectError("disk on fire", Code.Internal));

    await archiveWorktree("wt-feature");

    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("disk on fire"));
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("wt-feature");
    expect(useWorktreeStore.getState().deletingId).toBeNull();
  });

  it("deleteWorktree on a live workspace archives it, with the same switch to a fresh main chat", async () => {
    serve([mainRow, featureRow]);
    seedWorktreeList(projectId, [mainRow, featureRow]);
    useWorktreeStore.getState().selectWorktree({ id: "wt-feature", project_id: projectId });
    const switchSpy = vi.spyOn(useWorktreeStore.getState(), "switchWorktreeContext");

    await deleteWorktree("wt-feature");

    expect(server.calls("DeleteWorktree")).toBe(1);
    expect(getCachedWorktree("wt-feature")?.deleted_at).toBeTruthy();
    expect(switchSpy).toHaveBeenCalledWith(projectId, expect.objectContaining({ id: "wt-main" }), { openFreshNewChat: true });
    expect(toast.success).toHaveBeenCalledWith("Feature archived", expect.anything());
  });
});

describe("permanent delete", () => {
  it("removes an archived workspace from every list and its by-id entry", async () => {
    const archived = { ...featureRow, deleted_at: "2026-01-02T00:00:00.000Z" };
    serve([mainRow, archived]);
    seedWorktreeList(projectId, [mainRow, archived]);
    await resolveWorktree("wt-feature");

    await deleteWorktree("wt-feature");

    expect(server.rows.some((w) => w.id === "wt-feature")).toBe(false);
    expect(getCachedWorktree("wt-feature")).toBeUndefined();
    expect(queryClient.getQueryData(worktreeServiceKeys.getWorktree({ worktreeId: "wt-feature" }))).toBeUndefined();
    expect(toast.success).toHaveBeenCalledWith("Feature deleted permanently", expect.anything());
  });
});

describe("restore", () => {
  it("unarchives a workspace whose files are still there", async () => {
    const archived = { ...featureRow, deleted_at: "2026-01-02T00:00:00.000Z" };
    serve([mainRow, archived]);
    seedWorktreeList(projectId, [mainRow, archived]);

    await unarchiveWorktree("wt-feature");

    expect(server.calls("UnarchiveWorktree")).toBe(1);
    expect(server.calls("RecreateWorktree")).toBe(0);
    expect(getCachedWorktree("wt-feature")?.deleted_at).toBeFalsy();
    expect(toast.success).toHaveBeenCalledWith("Feature restored", expect.anything());
  });

  it("recreates a workspace whose files were removed, from its branch", async () => {
    const removed = {
      ...featureRow,
      deleted_at: "2026-01-02T00:00:00.000Z",
      cleanup_metadata: { directory_deleted: true, branch_deleted: false },
    };
    serve([mainRow, removed]);
    seedWorktreeList(projectId, [mainRow, removed]);

    await unarchiveWorktree("wt-feature");

    expect(server.calls("RecreateWorktree")).toBe(1);
    expect(server.calls("UnarchiveWorktree")).toBe(0);
    expect(getCachedWorktree("wt-feature")?.deleted_at).toBeFalsy();
  });

  it("warns when recreating could not put the saved work back", async () => {
    const removed = {
      ...featureRow,
      deleted_at: "2026-01-02T00:00:00.000Z",
      cleanup_metadata: { directory_deleted: true, branch_deleted: false },
    };
    serve([mainRow, removed]);
    seedWorktreeList(projectId, [mainRow, removed]);
    server.handlers["WorktreeService.RecreateWorktree"] = () => ({ message: "recreated", snapshotWarning: "conflicts in a.go" });

    await unarchiveWorktree("wt-feature");

    expect(toast.warning).toHaveBeenCalledWith(
      "Feature restored, but its saved work was not applied. conflicts in a.go",
      expect.anything(),
    );
  });

  it("throws (after a toast) when the restore fails", async () => {
    const archived = { ...featureRow, deleted_at: "2026-01-02T00:00:00.000Z" };
    serve([mainRow, archived]);
    seedWorktreeList(projectId, [mainRow, archived]);
    server.fail("UnarchiveWorktree", new ConnectError("no machine", Code.Unavailable));

    await expect(unarchiveWorktree("wt-feature")).rejects.toThrow(/no machine/);
    expect(toast.error).toHaveBeenCalled();
  });
});

describe("status", () => {
  it("updateWorktreeStatus writes the row every reader sees", async () => {
    serve([mainRow, featureRow]);
    await resolveWorktree("wt-feature");
    await updateWorktreeStatus("wt-feature", WorktreeStatus.MERGING);
    expect(getCachedWorktree("wt-feature")?.status).toBe(WorktreeStatus.MERGING);
  });
});

describe("selecting a project's workspace at start-up", () => {
  it("loadMainWorktree fetches one row and selects it, never listing the project", async () => {
    serve([mainRow, featureRow]);
    await loadMainWorktree(projectId);
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("wt-main");
    expect(server.requests("ListWorktrees")).toEqual([expect.objectContaining({ mainOnly: true, limit: 1 })]);
  });

  it("restoreLastWorktree selects main alone when nothing is saved", async () => {
    serve([mainRow, featureRow]);
    await expect(restoreLastWorktree(projectId)).resolves.toBe(true);
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("wt-main");
    expect(server.calls("GetWorktree")).toBe(0);
  });

  it("restoreLastWorktree fetches the saved workspace by id", async () => {
    serve([mainRow, featureRow]);
    useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-feature");
    await expect(restoreLastWorktree(projectId)).resolves.toBe(true);
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("wt-feature");
    expect(getCurrentWorktree()?.path).toBe("/tmp/feature");
  });

  it("restoreLastWorktree falls back to main when the saved workspace was archived, and forgets it", async () => {
    serve([mainRow, { ...featureRow, deleted_at: "2026-01-02T00:00:00.000Z" }]);
    useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-feature");
    await restoreLastWorktree(projectId);
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("wt-main");
    expect(useWorkspaceStateStore.getState().getProjectState(projectId).lastWorktreeId).toBeNull();
  });

  it("restoreLastWorktree keeps the saved workspace when the lookup merely failed", async () => {
    serve([mainRow, featureRow]);
    server.fail("GetWorktree", new ConnectError("offline", Code.Unavailable));
    useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-feature");
    await restoreLastWorktree(projectId);
    expect(useWorktreeStore.getState().currentWorktreeId).toBe("wt-main");
    expect(useWorkspaceStateStore.getState().getProjectState(projectId).lastWorktreeId).toBe("wt-feature");
  });

  it("restoreLastWorktree forgets a saved workspace the server says is gone", async () => {
    serve([mainRow]);
    useWorkspaceStateStore.getState().setLastWorktree(projectId, "wt-gone");
    await restoreLastWorktree(projectId);
    expect(isWorktreeKnownGone("wt-gone")).toBe(true);
    expect(useWorkspaceStateStore.getState().getProjectState(projectId).lastWorktreeId).toBeNull();
  });
});

describe("resolving one workspace by id", () => {
  it("answers from the cache without a request when it can", async () => {
    serve([mainRow, featureRow]);
    seedWorktreeList(projectId, [mainRow, featureRow]);
    await expect(resolveWorktree("wt-feature")).resolves.toMatchObject({ id: "wt-feature" });
    expect(server.calls("GetWorktree")).toBe(0);
  });

  it("fetches an unloaded workspace once even for concurrent callers", async () => {
    serve([mainRow, featureRow]);
    const [a, b] = await Promise.all([resolveWorktree("wt-feature"), resolveWorktree("wt-feature")]);
    expect(a?.id).toBe("wt-feature");
    expect(b?.id).toBe("wt-feature");
    expect(server.calls("GetWorktree")).toBe(1);
  });

  it("does not ask again for a workspace the server says is gone", async () => {
    serve([mainRow]);
    await expect(resolveWorktree("wt-gone")).resolves.toBeNull();
    await expect(resolveWorktree("wt-gone")).resolves.toBeNull();
    expect(server.calls("GetWorktree")).toBe(1);
  });

  it("asks again after a transient failure", async () => {
    serve([mainRow, featureRow]);
    server.fail("GetWorktree", new ConnectError("offline", Code.Unavailable));
    await expect(resolveWorktree("wt-feature")).resolves.toBeNull();
    server.fail("GetWorktree", null);
    await expect(resolveWorktree("wt-feature")).resolves.toMatchObject({ id: "wt-feature" });
  });
});
