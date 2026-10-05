// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox data layer: the badge reads counts only (limit 0), the list reads
 * items, both refetch on focus (failure kinds emit no user update), dismissing
 * takes the row out at once, and the user updates that change what is waiting
 * mark both stale.
 */

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import {
  InboxItemKind,
  InboxItemSchema,
  ListInboxResponseSchema,
} from "@/gen/reliant/v1/inbox_pb";
import { UserUpdateType } from "@/gen/reliant/v1/streaming_pb";

const listInbox = vi.fn();
const dismissInboxItem = vi.fn();
const restoreInboxItem = vi.fn();
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { inbox: () => ({ listInbox, dismissInboxItem, restoreInboxItem }) },
}));

import {
  inboxInvalidatingUpdate,
  inboxKeys,
  useDismissInboxItems,
  useInbox,
  useInboxCounts,
  useRestoreInboxItems,
} from "../inbox-queries";

function wrapperFor(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
}

const failing = create(InboxItemSchema, {
  kind: InboxItemKind.AUTOMATION_LAUNCH_FAILED,
  itemId: "automation_launch_failed:evt-1",
  triggerId: "trg-1",
  triggerName: "Nightly",
  payload: { case: "automationLaunchFailed", value: { reason: "no machine", eventId: "evt-1" } },
});

beforeEach(() => {
  listInbox.mockReset();
  dismissInboxItem.mockReset();
  restoreInboxItem.mockReset();
});

describe("useInboxCounts", () => {
  it("asks for counts only, and exposes the blocking count and the informational dot", async () => {
    listInbox.mockResolvedValue(
      create(ListInboxResponseSchema, { blockingCount: 3, hasInformational: true }),
    );
    const { result } = renderHook(() => useInboxCounts(), { wrapper: wrapperFor(newClient()) });
    await waitFor(() => expect(result.current.data).toEqual({ blockingCount: 3, hasInformational: true }));
    expect(listInbox.mock.calls[0]![0]).toMatchObject({ limit: 0 });
    expect(listInbox.mock.calls[0]![0].projectId).toBeUndefined();
  });

  it("scopes the counts to a project, under its own cache key", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, { blockingCount: 1 }));
    const queryClient = newClient();
    const { result } = renderHook(() => useInboxCounts("proj-1"), { wrapper: wrapperFor(queryClient) });
    await waitFor(() => expect(result.current.data?.blockingCount).toBe(1));
    expect(listInbox.mock.calls[0]![0]).toMatchObject({ limit: 0, projectId: "proj-1" });
    expect(queryClient.getQueryData(inboxKeys.counts("proj-1"))).toBeDefined();
    expect(queryClient.getQueryData(inboxKeys.counts())).toBeUndefined();
  });
});

describe("useInbox", () => {
  it("lists items and refetches on window focus even while fresh", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, { items: [failing], hasInformational: true }));
    const queryClient = newClient();
    const { result } = renderHook(() => useInbox(), { wrapper: wrapperFor(queryClient) });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(1));
    expect(listInbox.mock.calls[0]![0].limit).toBeUndefined();

    const query = queryClient.getQueryCache().find({ queryKey: inboxKeys.list() });
    expect((query?.options as { refetchOnWindowFocus?: unknown }).refetchOnWindowFocus).toBe("always");
  });
});

describe("useDismissInboxItems", () => {
  it("calls the RPC and removes the rows from every cached scope immediately", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, { items: [failing], hasInformational: true }));
    let resolveDismiss: () => void = () => {};
    dismissInboxItem.mockImplementation(() => new Promise<void>((resolve) => (resolveDismiss = resolve)));
    const queryClient = newClient();
    const wrapper = wrapperFor(queryClient);

    const list = renderHook(() => useInbox(), { wrapper });
    const scoped = renderHook(() => useInbox("proj-1"), { wrapper });
    await waitFor(() => expect(list.result.current.data?.items).toHaveLength(1));
    await waitFor(() => expect(scoped.result.current.data?.items).toHaveLength(1));

    const dismiss = renderHook(() => useDismissInboxItems(), { wrapper });
    act(() => dismiss.result.current.mutate(["automation_launch_failed:evt-1"]));

    await waitFor(() => expect(list.result.current.data?.items).toHaveLength(0));
    expect(scoped.result.current.data?.items).toHaveLength(0);
    expect(dismissInboxItem.mock.calls[0]![0]).toMatchObject({ itemIds: ["automation_launch_failed:evt-1"] });
    resolveDismiss();
  });

  it("puts every scope back when the RPC fails", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, { items: [failing], hasInformational: true }));
    dismissInboxItem.mockRejectedValue(new Error("nope"));
    const queryClient = newClient();
    // Keep the refetch after settle from masking the rollback.
    listInbox.mockResolvedValueOnce(create(ListInboxResponseSchema, { items: [failing] }));
    const wrapper = wrapperFor(queryClient);
    const list = renderHook(() => useInbox(), { wrapper });
    await waitFor(() => expect(list.result.current.data?.items).toHaveLength(1));

    const dismiss = renderHook(() => useDismissInboxItems(), { wrapper });
    act(() => dismiss.result.current.mutate(["automation_launch_failed:evt-1"]));
    await waitFor(() => expect(dismiss.result.current.isError).toBe(true));
    expect(list.result.current.data?.items).toHaveLength(1);
  });
});

describe("useRestoreInboxItems", () => {
  it("calls Restore with the ids and refetches", async () => {
    restoreInboxItem.mockResolvedValue({});
    const queryClient = newClient();
    queryClient.setQueryData(inboxKeys.list(), create(ListInboxResponseSchema, {}));
    const restore = renderHook(() => useRestoreInboxItems(), { wrapper: wrapperFor(queryClient) });
    act(() => restore.result.current.mutate(["approval:a-1"]));
    await waitFor(() => expect(restore.result.current.isSuccess).toBe(true));
    expect(restoreInboxItem.mock.calls[0]![0]).toMatchObject({ itemIds: ["approval:a-1"] });
    expect(queryClient.getQueryState(inboxKeys.list())?.isInvalidated).toBe(true);
  });
});

describe("inboxInvalidatingUpdate", () => {
  it.each([
    [UserUpdateType.CHAT_ACTIVITY_CHANGED, true],
    [UserUpdateType.CHAT_STATE_CHANGE, true],
    [UserUpdateType.CHAT_TITLE_CHANGED, false],
    [UserUpdateType.CHAT_CREATED, false],
  ])("update %s → %s", (type, expected) => {
    expect(inboxInvalidatingUpdate(type)).toBe(expected);
  });
});
