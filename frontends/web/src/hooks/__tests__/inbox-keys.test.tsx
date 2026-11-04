/**
 * The Inbox is InboxService on forge's generated keys (hooks/inbox-queries).
 * Every write is a generated mutation invalidating `inboxServiceKeys.all`, and
 * the stream invalidates the same root, so each of these refetches every
 * mounted scope (the current project's list and the all-projects one):
 *
 *   1. a dismiss refetches the lists (the rows left at once, the server's
 *      answer replaces them);
 *   2. a restore refetches the lists;
 *   3. a storage clean-up refetches the lists, and so does a failed one;
 *   4. the stream's inbox-invalidating update reaches `inboxServiceKeys.all`
 *      (STREAM_INVALIDATED_SCOPES.inbox), so the reconnect sweep covers it.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { UserUpdateType, EntityType } from "@/gen/reliant/v1/streaming_pb";
import type { UserUpdate } from "@/types/streaming";

vi.mock("@/api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

import { useCleanupStorage, useDismissInboxItems, useInbox, useRestoreInboxItems } from "../inbox-queries";
import { inboxServiceKeys } from "../inbox-service-hooks_gen";
import { STREAM_INVALIDATED_SCOPES } from "@/store/streamInvalidatedScopes";
import { resetStreamInvalidation, useGlobalUpdatesStore } from "@/store/globalUpdatesStore";

let waiting: string[];
let listCalls: number;
const cleanupStorage = vi.fn(async () => ({ results: [] as unknown[] }));

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  waiting = ["approval:a-1", "question:q-1"];
  listCalls = 0;
  cleanupStorage.mockReset().mockResolvedValue({ results: [] });
  setTransport(
    mockTransport(
      serviceHandlers("InboxService", {
        listInbox: () => {
          listCalls++;
          return { items: waiting.map((itemId) => ({ itemId })), blockingCount: waiting.length };
        },
        dismissInboxItem: (req: { itemIds: string[] }) => {
          waiting = waiting.filter((id) => !req.itemIds.includes(id));
          return {};
        },
        restoreInboxItem: (req: { itemIds: string[] }) => {
          waiting = [...waiting, ...req.itemIds];
          return {};
        },
        cleanupStorage: () => cleanupStorage(),
      }),
    ),
  );
});

afterEach(() => {
  resetStreamInvalidation();
  setTransport(null);
  queryClient.clear();
});

const ids = (data: { items: { itemId: string }[] } | undefined) => data?.items.map((item) => item.itemId);

function mountBothScopes() {
  return renderHook(() => ({ all: useInbox(), project: useInbox("proj-1") }), { wrapper });
}

describe("Inbox keys", () => {
  it("a dismiss refetches every mounted scope", async () => {
    const lists = mountBothScopes();
    await waitFor(() => expect(ids(lists.result.current.project.data)).toEqual(["approval:a-1", "question:q-1"]));
    expect(ids(lists.result.current.all.data)).toEqual(["approval:a-1", "question:q-1"]);
    expect(listCalls).toBe(2);

    const dismiss = renderHook(() => useDismissInboxItems(), { wrapper });
    await act(() => dismiss.result.current.mutateAsync({ itemIds: ["approval:a-1"] }));

    await waitFor(() => expect(listCalls).toBe(4));
    expect(ids(lists.result.current.all.data)).toEqual(["question:q-1"]);
    expect(ids(lists.result.current.project.data)).toEqual(["question:q-1"]);
  });

  it("a restore refetches every mounted scope", async () => {
    waiting = [];
    const lists = mountBothScopes();
    await waitFor(() => expect(listCalls).toBe(2));

    const restore = renderHook(() => useRestoreInboxItems(), { wrapper });
    await act(() => restore.result.current.mutateAsync({ itemIds: ["approval:a-1"] }));

    await waitFor(() => expect(ids(lists.result.current.all.data)).toEqual(["approval:a-1"]));
    expect(ids(lists.result.current.project.data)).toEqual(["approval:a-1"]);
  });

  it("a storage clean-up refetches the lists, whether it succeeds or fails", async () => {
    const lists = mountBothScopes();
    await waitFor(() => expect(listCalls).toBe(2));
    const cleanup = renderHook(() => useCleanupStorage(), { wrapper });

    await act(() => cleanup.result.current.mutateAsync({ daemonId: "d-1", worktreeIds: ["wt-1"] }));
    await waitFor(() => expect(listCalls).toBe(4));

    cleanupStorage.mockRejectedValueOnce(new Error("machine offline"));
    await act(async () => {
      await cleanup.result.current
        .mutateAsync({ daemonId: "d-1", worktreeIds: ["wt-1"] })
        .catch(() => undefined);
    });
    await waitFor(() => expect(listCalls).toBe(6));
    expect(lists.result.current.all.isSuccess).toBe(true);
  });

  it("the stream's inbox-invalidating update reaches inboxServiceKeys.all", () => {
    expect(STREAM_INVALIDATED_SCOPES.inbox).toEqual(inboxServiceKeys.all);

    queryClient.setQueryData(inboxServiceKeys.listInbox({}), { items: [] });
    queryClient.setQueryData(inboxServiceKeys.listInbox({ projectId: "p1" }), { items: [] });
    const update: UserUpdate = {
      id: "u-1",
      user_id: "user-1",
      sequence_number: 1,
      chat_id: "c-1",
      project_id: "p1",
      update_type: UserUpdateType.REFETCH,
      entity_type: EntityType.CHAT,
      entity_id: "c-1",
      data: { type: "inbox" },
      created_at: new Date().toISOString(),
    };
    useGlobalUpdatesStore.getState().handleUpdate([update]);

    expect(queryClient.getQueryState(inboxServiceKeys.listInbox({}))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(inboxServiceKeys.listInbox({ projectId: "p1" }))?.isInvalidated).toBe(true);
  });
});
