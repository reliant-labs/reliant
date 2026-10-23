/**
 * RepoService reads and writes share the generated key scope: a repo
 * mutation refetches the cached ListRepos answer, so a component holding the
 * list (the create-workspace modal, the mobile sheet) never shows a stale one.
 *
 * The web calls no repo mutation and no stream event names repos today; this
 * pins the contract the day either arrives.
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { repoServiceKeys, useAddRepo, useListRepos } from "../repo-service-hooks_gen";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: 0 }, mutations: { retry: false } },
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

function repoTransport() {
  const repos = [{ id: "r1", relativePath: "api" }];
  let listCalls = 0;
  const transport = mockTransport({
    "RepoService.ListRepos": () => {
      listCalls++;
      return { repos: [...repos] };
    },
    "RepoService.AddRepo": (req) => {
      const { relativePath } = req as { relativePath: string };
      repos.push({ id: `r${repos.length + 1}`, relativePath });
      return {};
    },
  });
  return { transport, listCalls: () => listCalls };
}

describe("RepoService keys", () => {
  it("AddRepo refetches the cached repo list", async () => {
    const { transport, listCalls } = repoTransport();
    setTransport(transport);

    const list = renderHook(() => useListRepos({ projectId: "p1" }), { wrapper });
    await waitFor(() => expect(list.result.current.data?.repos).toHaveLength(1));

    const add = renderHook(() => useAddRepo(), { wrapper });
    await add.result.current.mutateAsync({ projectId: "p1", relativePath: "web" });

    await waitFor(() => expect(list.result.current.data?.repos.map((r) => r.relativePath)).toEqual(["api", "web"]));
    expect(listCalls()).toBe(2);
  });

  it("invalidating repoServiceKeys.all reaches every ListRepos entry", async () => {
    const { transport, listCalls } = repoTransport();
    setTransport(transport);

    const list = renderHook(() => useListRepos({ projectId: "p1" }), { wrapper });
    await waitFor(() => expect(list.result.current.isSuccess).toBe(true));

    await queryClient.invalidateQueries({ queryKey: repoServiceKeys.all });
    await waitFor(() => expect(listCalls()).toBe(2));
  });
});
