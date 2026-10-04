// Copyright (c) 2025 Reliant Labs

/**
 * The Runs list's data layer: pages chain on next_page_token and append in
 * order, and the user-update events that change a run (state, activity, a new
 * chat) make the list stale.
 */

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import { ListRunsResponseSchema, RunSchema, RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { UserUpdateType } from "@/gen/reliant/v1/streaming_pb";

const listRuns = vi.fn();
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { run: () => ({ listRuns }) },
}));

import { runKeys, runsInvalidatingUpdate, useRunList } from "../run-queries";

function run(id: string) {
  return create(RunSchema, {
    id: `wf-${id}`,
    sessionId: id,
    title: `Run ${id}`,
    displayState: RunDisplayState.COMPLETED,
    createdAtMs: BigInt(Date.now()),
  });
}

function wrapperFor(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

describe("useRunList", () => {
  beforeEach(() => listRuns.mockReset());

  it("loads the first page, then appends the next page using its token", async () => {
    listRuns
      .mockResolvedValueOnce(create(ListRunsResponseSchema, { runs: [run("a"), run("b")], nextPageToken: "tok-2" }))
      .mockResolvedValueOnce(create(ListRunsResponseSchema, { runs: [run("c")], nextPageToken: "" }));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    const { result } = renderHook(() => useRunList({ kind: ["schedule"] }), {
      wrapper: wrapperFor(queryClient),
    });

    await waitFor(() => expect(result.current.runs.map((r) => r.chatId)).toEqual(["a", "b"]));
    expect(result.current.hasNextPage).toBe(true);
    expect(listRuns.mock.calls[0]![0]).toMatchObject({ launchKind: ["schedule"] });
    expect(listRuns.mock.calls[0]![0].pageToken).toBeUndefined();

    await act(async () => {
      await result.current.fetchNextPage();
    });

    await waitFor(() => expect(result.current.runs.map((r) => r.chatId)).toEqual(["a", "b", "c"]));
    expect(listRuns.mock.calls[1]![0]).toMatchObject({ pageToken: "tok-2", launchKind: ["schedule"] });
    expect(result.current.hasNextPage).toBe(false);
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

  it("runKeys.lists() is a prefix of every list key, so one invalidation reaches every filter set", () => {
    const listKey = runKeys.list({ kind: ["schedule"] });
    expect(listKey.slice(0, runKeys.lists().length)).toEqual([...runKeys.lists()]);
  });
});
