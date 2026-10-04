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
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { inbox: () => ({ listInbox, dismissInboxItem }) },
}));

import {
  inboxInvalidatingUpdate,
  inboxKeys,
  useDismissInboxItem,
  useInbox,
  useInboxCounts,
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
  itemId: "launch-failed:evt-1",
  triggerId: "trg-1",
  triggerName: "Nightly",
  payload: { case: "automationLaunchFailed", value: { reason: "no machine", eventId: "evt-1" } },
});

beforeEach(() => {
  listInbox.mockReset();
  dismissInboxItem.mockReset();
});

describe("useInboxCounts", () => {
  it("asks for counts only, and exposes the blocking count and the informational dot", async () => {
    listInbox.mockResolvedValue(
      create(ListInboxResponseSchema, { blockingCount: 3, hasInformational: true }),
    );
    const { result } = renderHook(() => useInboxCounts(), { wrapper: wrapperFor(newClient()) });
    await waitFor(() => expect(result.current.data).toEqual({ blockingCount: 3, hasInformational: true }));
    expect(listInbox.mock.calls[0]![0]).toMatchObject({ limit: 0 });
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

describe("useDismissInboxItem", () => {
  it("calls the RPC and removes the row from the cached list immediately", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, { items: [failing], hasInformational: true }));
    let resolveDismiss: () => void = () => {};
    dismissInboxItem.mockImplementation(() => new Promise<void>((resolve) => (resolveDismiss = resolve)));
    const queryClient = newClient();
    const wrapper = wrapperFor(queryClient);

    const list = renderHook(() => useInbox(), { wrapper });
    await waitFor(() => expect(list.result.current.data?.items).toHaveLength(1));

    const dismiss = renderHook(() => useDismissInboxItem(), { wrapper });
    act(() => dismiss.result.current.mutate("launch-failed:evt-1"));

    await waitFor(() => expect(list.result.current.data?.items).toHaveLength(0));
    expect(dismissInboxItem.mock.calls[0]![0]).toMatchObject({ itemId: "launch-failed:evt-1" });
    resolveDismiss();
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
