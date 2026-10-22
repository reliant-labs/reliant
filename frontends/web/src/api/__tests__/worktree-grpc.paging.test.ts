import { beforeEach, describe, expect, it, vi } from "vitest";

const listWorktrees = vi.hoisted(() => vi.fn());
const listArchivedWorktrees = vi.hoisted(() => vi.fn());

vi.mock("../grpc-client", () => ({
  grpcClient: { worktree: () => ({ listWorktrees, listArchivedWorktrees }) },
}));

import { worktreeGrpc } from "../worktree-grpc";

const row = (id: string, isMain = false) => ({ id, name: id, path: `/w/${id}`, projectId: "p1", isMain });

describe("worktreeGrpc paging", () => {
  beforeEach(() => {
    listWorktrees.mockReset();
    listArchivedWorktrees.mockReset();
  });

  it("list returns one page and its continuation token, never the whole project", async () => {
    listWorktrees.mockResolvedValue({ worktrees: [row("a"), row("b")], total: 2, nextPageToken: "t2" });

    const page = await worktreeGrpc.list("p1");

    expect(listWorktrees).toHaveBeenCalledTimes(1);
    expect(page.worktrees.map((w) => w.id)).toEqual(["a", "b"]);
    expect(page.nextPageToken).toBe("t2");
  });

  it("list passes the page token and never asks for archived rows", async () => {
    listWorktrees.mockResolvedValue({ worktrees: [], total: 0, nextPageToken: "" });

    await worktreeGrpc.list("p1", { pageToken: "t2" });

    const request = listWorktrees.mock.calls[0]![0];
    expect(request.pageToken).toBe("t2");
    expect(request).not.toHaveProperty("includeArchived");
  });

  it("getMain asks the server for the main workspace alone", async () => {
    listWorktrees.mockResolvedValue({ worktrees: [row("wt-main", true)], total: 1, nextPageToken: "" });

    const main = await worktreeGrpc.getMain("p1");

    expect(main?.id).toBe("wt-main");
    expect(listWorktrees.mock.calls[0]![0]).toMatchObject({ mainOnly: true, limit: 1 });
  });

  it("getMain is null when the server returns no main workspace", async () => {
    listWorktrees.mockResolvedValue({ worktrees: [], total: 0, nextPageToken: "" });
    expect(await worktreeGrpc.getMain("p1")).toBeNull();
  });

  it("listArchived is its own RPC and pages by token", async () => {
    listArchivedWorktrees.mockResolvedValue({ worktrees: [row("old")], total: 1, nextPageToken: "t9" });

    const page = await worktreeGrpc.listArchived("p1", { pageToken: "t8" });

    expect(listWorktrees).not.toHaveBeenCalled();
    expect(listArchivedWorktrees.mock.calls[0]![0]).toMatchObject({ projectId: "p1", pageToken: "t8" });
    expect(page.worktrees.map((w) => w.id)).toEqual(["old"]);
    expect(page.nextPageToken).toBe("t9");
  });
});
