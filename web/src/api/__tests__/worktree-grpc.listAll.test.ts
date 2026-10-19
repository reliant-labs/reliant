import { beforeEach, describe, expect, it, vi } from "vitest";

const listWorktrees = vi.hoisted(() => vi.fn());

vi.mock("../grpc-client", () => ({
  grpcClient: { worktree: () => ({ listWorktrees }) },
}));

import { worktreeGrpc } from "../worktree-grpc";

const row = (id: string) => ({ id, name: id, path: `/w/${id}`, projectId: "p1", isMain: id === "wt-main" });

// Serves `total` worktrees `pageSize` at a time with page tokens, like the server.
function serve(total: number, pageSize: number) {
  const all = Array.from({ length: total }, (_, i) => row(i === total - 1 ? "wt-main" : `wt-${i}`));
  listWorktrees.mockImplementation(async (req: { limit: number; pageToken?: string }) => {
    const start = req.pageToken ? Number(req.pageToken) : 0;
    const end = Math.min(start + (req.limit || pageSize), total);
    return {
      worktrees: all.slice(start, end),
      total: end - start,
      nextPageToken: end < total ? String(end) : "",
    };
  });
}

describe("worktreeGrpc.listAll", () => {
  beforeEach(() => {
    listWorktrees.mockReset();
  });

  it("follows page tokens past the first 100 worktrees, main included", async () => {
    serve(113, 100);

    const all = await worktreeGrpc.listAll("p1");

    expect(all).toHaveLength(113);
    expect(listWorktrees).toHaveBeenCalledTimes(2);
    expect(all.find((w) => w.is_main)?.id).toBe("wt-main");
  });

  it("makes one request when everything fits on a page", async () => {
    serve(3, 100);
    expect(await worktreeGrpc.listAll("p1")).toHaveLength(3);
    expect(listWorktrees).toHaveBeenCalledTimes(1);
  });

  it("does not duplicate a worktree that shifts between pages", async () => {
    listWorktrees
      .mockResolvedValueOnce({ worktrees: [row("a"), row("b")], total: 2, nextPageToken: "t1" })
      .mockResolvedValueOnce({ worktrees: [row("b"), row("c")], total: 2, nextPageToken: "" });

    expect((await worktreeGrpc.listAll("p1")).map((w) => w.id)).toEqual(["a", "b", "c"]);
  });

  it("fails loudly instead of looping when the server never ends the list", async () => {
    listWorktrees.mockImplementation(async () => ({ worktrees: [], total: 0, nextPageToken: "again" }));
    await expect(worktreeGrpc.listAll("p1")).rejects.toThrow(/did not finish/);
  });
});
