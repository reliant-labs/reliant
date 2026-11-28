import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";

const { searchLogs } = vi.hoisted(() => ({ searchLogs: vi.fn() }));

vi.mock("@reliantlabs/forge-web-runtime/transport-scope", () => ({
  scopedClient: () => () => ({ searchLogs }),
}));
vi.mock("@/lib/connect", () => ({ connectClient: () => ({}) }));
vi.mock("@/services/controlPlane/observability/obsInterceptors", () => ({ obsCall: () => ({}) }));
vi.mock("@/services/controlPlane/observability/ObsQuotaContext", () => ({
  useObsQuota: () => ({ polls: { subscribe: () => () => {} } }),
}));

import { useSearchLogs } from "./observability-queries";
import { effectiveWindowOf } from "@/services/controlPlane/observability/api";

afterEach(() => {
  vi.restoreAllMocks();
  searchLogs.mockReset();
});

it("refetches paged log history over a new consistent interval", async () => {
  const initialNow = 1_800_000_000_000;
  const clock = vi.spyOn(Date, "now").mockReturnValue(initialNow);
  // An older server does not echo effective_time_range. It must still receive
  // identical absolute bounds on both pages, even if time moves between them.
  searchLogs.mockImplementation(async (request) => ({
    logs: [],
    nextPageToken: request.pageToken ? "" : `cursor-${Date.now()}`,
  }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  const hook = renderHook(() => useSearchLogs(
    { orgId: "org", envIds: [], filters: [] },
    { range: "1h", query: "", services: [], severities: [] },
    false,
  ), { wrapper });
  try {
    await waitFor(() => expect(hook.result.current.isSuccess).toBe(true));
    expect(searchLogs).toHaveBeenCalledTimes(1);
    expect(hook.result.current.hasNextPage).toBe(true);
    clock.mockReturnValue(initialNow + 10_000);
    await act(async () => { await hook.result.current.fetchNextPage(); });
    await waitFor(() => expect(hook.result.current.data?.pages).toHaveLength(2));
    const first = searchLogs.mock.calls[0][0];
    const second = searchLogs.mock.calls[1][0];
    const originalWindow = effectiveWindowOf({ effectiveTimeRange: first.scope.timeRange });
    expect(originalWindow).toEqual({ startMs: initialNow - 3_600_000, endMs: initialNow });
    expect(second.scope.timeRange).toEqual(first.scope.timeRange);

    const refetchNow = initialNow + 60_000;
    clock.mockReturnValue(refetchNow);
    await act(async () => { await hook.result.current.refetch(); });
    expect(searchLogs).toHaveBeenCalledTimes(4);
    const refreshedFirst = searchLogs.mock.calls[2][0];
    const refreshedSecond = searchLogs.mock.calls[3][0];
    expect(refreshedFirst.pageToken).toBe("");
    expect(effectiveWindowOf({ effectiveTimeRange: refreshedFirst.scope.timeRange })).toEqual({
      startMs: refetchNow - 3_600_000,
      endMs: refetchNow,
    });
    expect(refreshedSecond.scope.timeRange).toEqual(refreshedFirst.scope.timeRange);
    expect(refreshedSecond.pageToken).toBe(`cursor-${refetchNow}`);
    expect(hook.result.current.data?.pages).toHaveLength(2);
  } finally {
    hook.unmount();
    client.clear();
  }
});
