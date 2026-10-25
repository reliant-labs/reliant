// Copyright (c) 2025 Reliant Labs

/**
 * The Runs list's data layer: pages chain on next_page_token and append in
 * order, a remount finds the list it left in the cache, and the user-update
 * events that change a run (state, activity, a new chat) make it stale.
 */

import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { RunDisplayState, type ListRunsRequest } from "@/gen/reliant/v1/run_pb";
import { UserUpdateType } from "@/gen/reliant/v1/streaming_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { runsInvalidatingUpdate, useRunList } from "../run-queries";
import { runServiceKeys } from "../run-service-hooks_gen";

const listRuns = vi.fn<(request: ListRunsRequest) => unknown>();

beforeEach(() => {
  listRuns.mockReset();
  setTransport(mockTransport({ "RunService.ListRuns": (request) => listRuns(request as ListRunsRequest) }));
});

afterEach(() => setTransport(null));

function run(id: string) {
  return {
    id: `wf-${id}`,
    sessionId: id,
    title: `Run ${id}`,
    displayState: RunDisplayState.COMPLETED,
    createdAtMs: BigInt(Date.now()),
  };
}

function wrapperFor(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

describe("useRunList", () => {
  it("loads the first page, then appends the next page using its token", async () => {
    listRuns
      .mockResolvedValueOnce({ runs: [run("a"), run("b")], nextPageToken: "tok-2" })
      .mockResolvedValueOnce({ runs: [run("c")], nextPageToken: "" });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    const { result } = renderHook(() => useRunList({ kind: ["schedule"] }), {
      wrapper: wrapperFor(queryClient),
    });

    await waitFor(() => expect(result.current.runs.map((r) => r.chatId)).toEqual(["a", "b"]));
    expect(result.current.hasNextPage).toBe(true);
    expect(listRuns.mock.calls[0]![0]).toMatchObject({ launchKind: ["schedule"], limit: 50 });
    expect(listRuns.mock.calls[0]![0].pageToken).toBeUndefined();

    await act(async () => {
      await result.current.fetchNextPage();
    });

    await waitFor(() => expect(result.current.runs.map((r) => r.chatId)).toEqual(["a", "b", "c"]));
    const second = listRuns.mock.calls[1]![0];
    expect(second).toMatchObject({ pageToken: "tok-2", launchKind: ["schedule"] });
    // The next page pages through the same window as the first.
    expect(second.startedAfter).toEqual(listRuns.mock.calls[0]![0].startedAfter);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("asks a remounted list for the window it already holds, so the cached rows show at once", async () => {
    listRuns.mockResolvedValue({ runs: [run("a")], nextPageToken: "" });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    const first = renderHook(() => useRunList({ kind: ["webhook"] }), { wrapper: wrapperFor(queryClient) });
    await waitFor(() => expect(first.result.current.runs).toHaveLength(1));
    first.unmount();

    const second = renderHook(() => useRunList({ kind: ["webhook"] }), { wrapper: wrapperFor(queryClient) });
    expect(second.result.current.runs.map((r) => r.chatId)).toEqual(["a"]);
    await waitFor(() => expect(listRuns).toHaveBeenCalledTimes(2));
    expect(listRuns.mock.calls[1]![0].startedAfter).toEqual(listRuns.mock.calls[0]![0].startedAfter);
  });
});

describe("runsInvalidatingUpdate", () => {
  it.each([
    [UserUpdateType.CHAT_STATE_CHANGE, true],
    [UserUpdateType.CHAT_ACTIVITY_CHANGED, true],
    [UserUpdateType.CHAT_CREATED, true],
    [UserUpdateType.CHAT_TITLE_CHANGED, false],
    [UserUpdateType.PROCESS_STARTED, false],
  ] as const)("update type %s invalidates runs: %s", (type, expected) => {
    expect(runsInvalidatingUpdate(type)).toBe(expected);
  });

  it("runServiceKeys.all is a prefix of every list key, so one invalidation reaches every filter set", () => {
    const listKey = runServiceKeys.listRunsInfinite({ launchKind: ["schedule"] });
    expect(listKey.slice(0, runServiceKeys.all.length)).toEqual([...runServiceKeys.all]);
  });
});
