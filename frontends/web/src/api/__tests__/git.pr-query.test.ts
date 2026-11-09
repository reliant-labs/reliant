import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { worktreeServiceKeys } from "@/hooks/worktree-service-hooks_gen";
import { getExistingPR } from "../git";

afterEach(() => { queryClient.clear(); setTransport(null); });

const found = { exists: true, url: "https://github.test/o/r/pull/7", number: 7, title: "Seven", state: "OPEN" };

describe("imperative git PR read on the generated query definition", () => {
  it("shares concurrent identical requests and keeps the existing public result shape", async () => {
    let finish!: (response: typeof found) => void;
    const lookup = vi.fn((_request: unknown) => new Promise<typeof found>((resolve) => { finish = resolve; }));
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    const first = getExistingPR("wt-a", "nested-a");
    const second = getExistingPR("wt-a", "nested-a");
    await vi.waitFor(() => expect(lookup).toHaveBeenCalledTimes(1));
    expect(lookup.mock.calls[0]?.[0]).toMatchObject({ worktreeId: "wt-a", repoId: "nested-a" });
    finish(found);
    expect(await first).toEqual(found);
    expect(await second).toEqual(found);
    expect(queryClient.getQueryState(worktreeServiceKeys.getWorktreePR({ worktreeId: "wt-a", repoId: "nested-a" }))?.data).toMatchObject(found);
  });

  it("revalidates even a negative cached result and preserves empty-to-undefined mapping", async () => {
    const lookup = vi.fn().mockReturnValueOnce({ exists: false }).mockReturnValue(found);
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    expect(await getExistingPR("wt-a")).toEqual({ exists: false, url: undefined, number: undefined, title: undefined, state: undefined });
    expect(await getExistingPR("wt-a")).toEqual(found);
    expect(lookup).toHaveBeenCalledTimes(2);
    expect(lookup.mock.calls[0]?.[0]).toMatchObject({ repoId: "" });
  });

  it("isolates worktrees and nested repositories, including pending reads", async () => {
    const lookup = vi.fn((request: unknown) => ({ ...found, title: JSON.stringify(request) }));
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    const results = await Promise.all([getExistingPR("wt-a", "repo-a"), getExistingPR("wt-a", "repo-b"), getExistingPR("wt-b", "repo-a")]);
    expect(lookup).toHaveBeenCalledTimes(3);
    expect(new Set(results.map((result) => result.title)).size).toBe(3);
  });

  it("rejects once without retrying, side effects or serving a stale cached success", async () => {
    const key = worktreeServiceKeys.getWorktreePR({ worktreeId: "wt-a", repoId: "repo-a" });
    queryClient.setQueryData(key, found);
    const lookup = vi.fn(() => { throw new ConnectError("remote unavailable", Code.Unavailable); });
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    await expect(getExistingPR("wt-a", "repo-a")).rejects.toMatchObject({ code: Code.Unavailable });
    expect(lookup).toHaveBeenCalledTimes(1);
    expect(queryClient.getMutationCache().getAll()).toEqual([]);
  });
});
