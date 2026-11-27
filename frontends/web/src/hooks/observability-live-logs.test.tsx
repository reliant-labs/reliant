import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

import { SearchLogsResponseSchema } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { useSearchLogs } from "./observability-queries";

const { searchLogs } = vi.hoisted(() => ({ searchLogs: vi.fn() }));
vi.mock("@reliantlabs/forge-web-runtime/transport-scope", () => ({
  scopedClient: () => () => ({ searchLogs }),
}));

afterEach(() => {
  vi.restoreAllMocks();
  searchLogs.mockReset();
});

it("keeps live refreshes on one fresh head after paging history", async () => {
  let now = 1_800_000_000_000;
  vi.spyOn(Date, "now").mockImplementation(() => now);
  searchLogs.mockImplementation(async ({ pageToken }) => create(SearchLogsResponseSchema, { nextPageToken: pageToken ? "" : "older" }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { result, rerender, unmount } = renderHook(
    ({ live }) => useSearchLogs(
      { orgId: "org", envIds: [], filters: [] },
      { range: "1h", query: "", services: [], severities: [] },
      live,
    ),
    { wrapper, initialProps: { live: false } },
  );
  try {
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(searchLogs).toHaveBeenCalledTimes(1);
    expect(result.current.hasNextPage).toBe(true);
    await act(async () => { await result.current.fetchNextPage(); });
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));

    now += 60_000;
    rerender({ live: true });
    expect(result.current.isPlaceholderData).toBe(true);
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.data?.pageParams).toEqual([null]);
    const liveCache = client.getQueryCache().findAll().find((entry) => entry.queryKey.at(-1) === "live");
    expect(liveCache).toBeDefined();
    expect(liveCache?.state.data).toBeUndefined();
    await waitFor(() => expect(result.current.isPlaceholderData).toBe(false));
    expect(result.current.data?.pages[0]?.window.endMs).toBe(now);
    expect(result.current.hasNextPage).toBe(false);
    const beforePaging = searchLogs.mock.calls.length;
    await act(async () => { await result.current.fetchNextPage(); });
    expect(searchLogs).toHaveBeenCalledTimes(beforePaging);
    expect(result.current.data?.pages).toHaveLength(1);

    for (let tick = 0; tick < 3; tick++) {
      now += 5_000;
      const before = searchLogs.mock.calls.length;
      await act(async () => { await client.invalidateQueries(); });
      expect(searchLogs).toHaveBeenCalledTimes(before + 1);
      const request = searchLogs.mock.calls.at(-1)![0];
      expect(request.pageToken ?? "").toBe("");
      await waitFor(() => expect(result.current.data?.pages[0]?.window.endMs).toBe(now));
      expect(result.current.data?.pages).toHaveLength(1);
      expect(result.current.data?.pageParams).toEqual([null]);
    }

    rerender({ live: false });
    expect(result.current.data?.pages).toHaveLength(2);
  } finally {
    unmount();
    client.clear();
  }
});

it.each([
  { orgId: "other-org", query: "" },
  { orgId: "org", query: "something else" },
])("does not show history when switching scope or filters: %j", async (next) => {
  searchLogs.mockResolvedValue(create(SearchLogsResponseSchema));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { result, rerender, unmount } = renderHook(
    ({ orgId, query, live }) => useSearchLogs(
      { orgId, envIds: [], filters: [] },
      { range: "1h", query, services: [], severities: [] },
      live,
    ),
    { wrapper, initialProps: { orgId: "org", query: "", live: false } },
  );
  try {
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(1));
    rerender({ ...next, live: true });
    expect(result.current.isPlaceholderData).toBe(false);
    expect(result.current.data).toBeUndefined();
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(1));
  } finally {
    unmount();
    client.clear();
  }
});
