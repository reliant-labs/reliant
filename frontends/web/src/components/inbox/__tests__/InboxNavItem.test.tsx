// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox nav item's badge (WORKFLOW_UI.md §8.2, §8.3): blocking items as a
 * number, failures as a dot, an aria-live count, and — if the counts cannot
 * be read — the client-known count of listed chats awaiting input with a
 * warning dot that says it may be incomplete.
 */

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import { ListInboxResponseSchema } from "@/gen/reliant/v1/inbox_pb";
import { ChatActivity } from "@/gen/reliant/v1/chat_pb";

const listInbox = vi.hoisted(() => vi.fn());
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { inbox: () => ({ listInbox }) },
}));

import { InboxNavItem } from "../InboxNavItem";
import { useActivityStore } from "@/store/activityStore";

function renderItem(ui: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  listInbox.mockReset();
  useActivityStore.setState({ entries: new Map(), activities: new Map(), maxSeenSeq: 0 });
});

describe("InboxNavItem", () => {
  it("shows blocking_count as a number and a dot for informational items", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, { blockingCount: 4, hasInformational: true }));
    renderItem(<InboxNavItem onOpen={vi.fn()} />);
    expect(await screen.findByTestId("inbox-badge-count")).toHaveTextContent("4");
    expect(screen.getByTestId("inbox-badge-dot")).toBeInTheDocument();
    expect(listInbox.mock.calls[0]![0]).toMatchObject({ limit: 0 });
  });

  it("a count without failures has no dot; failures alone are a dot without a number", async () => {
    listInbox.mockResolvedValueOnce(create(ListInboxResponseSchema, { blockingCount: 2, hasInformational: false }));
    const first = renderItem(<InboxNavItem onOpen={vi.fn()} />);
    expect(await screen.findByTestId("inbox-badge-count")).toHaveTextContent("2");
    expect(screen.queryByTestId("inbox-badge-dot")).toBeNull();
    first.unmount();

    listInbox.mockResolvedValueOnce(create(ListInboxResponseSchema, { blockingCount: 0, hasInformational: true }));
    renderItem(<InboxNavItem onOpen={vi.fn()} />);
    expect(await screen.findByTestId("inbox-badge-dot")).toBeInTheDocument();
    expect(screen.queryByTestId("inbox-badge-count")).toBeNull();
  });

  it("nothing waiting: no badge at all", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, {}));
    renderItem(<InboxNavItem onOpen={vi.fn()} />);
    await waitFor(() => expect(listInbox).toHaveBeenCalled());
    expect(screen.queryByTestId("inbox-badge-count")).toBeNull();
    expect(screen.queryByTestId("inbox-badge-dot")).toBeNull();
  });

  it("announces the count politely and names it in the button's label", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, { blockingCount: 1, hasInformational: false }));
    renderItem(<InboxNavItem onOpen={vi.fn()} />);
    const live = await screen.findByTestId("inbox-badge-live");
    expect(live).toHaveAttribute("aria-live", "polite");
    await waitFor(() => expect(live).toHaveTextContent("1 item needs you"));
    expect(screen.getByRole("button", { name: /Inbox/ })).toBeInTheDocument();
  });

  it("calls onOpen when clicked", async () => {
    listInbox.mockResolvedValue(create(ListInboxResponseSchema, {}));
    const onOpen = vi.fn();
    renderItem(<InboxNavItem onOpen={onOpen} />);
    fireEvent.click(screen.getByRole("button", { name: /Inbox/ }));
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it("on error falls back to the client-known awaiting count with a warning dot", async () => {
    listInbox.mockRejectedValue(new Error("down"));
    useActivityStore.getState().setActivity("chat-a", ChatActivity.AWAITING_INPUT);
    useActivityStore.getState().setActivity("chat-b", ChatActivity.AWAITING_INPUT);
    useActivityStore.getState().setActivity("chat-c", ChatActivity.RUNNING);
    renderItem(<InboxNavItem onOpen={vi.fn()} />);
    expect(await screen.findByTestId("inbox-badge-warning")).toBeInTheDocument();
    expect(screen.getByTestId("inbox-badge-count")).toHaveTextContent("2");
  });
});
