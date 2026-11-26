// Copyright (c) 2026 Reliant Labs

/**
 * A machine's page lists that machine's housekeeping — the items the Inbox
 * keeps out by default — from one ListInbox read that asks for every
 * housekeeping (and alert) item, filtered to the machine by daemon id. Each
 * row is the Inbox's own row, so its actions work here.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  InboxCategory,
  InboxItemKind,
  InboxItemSchema,
  ListInboxResponseSchema,
  type InboxItem,
} from "@/gen/reliant/v1/inbox_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { approvalItem, renderInboxAt, storageItem } from "@/components/inbox/__tests__/inboxTestUtils";

const mocks = vi.hoisted(() => ({
  listInbox: vi.fn(),
  keepMarkedWorktree: vi.fn(),
  dismissInboxItem: vi.fn(),
  notify: vi.fn(),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/toast-manager", () => ({ toast: { notify: mocks.notify, error: vi.fn() } }));

import { MachineHousekeepingCard } from "../MachineHousekeepingCard";

function markedItem(daemonId: string, name: string): InboxItem {
  return create(InboxItemSchema, {
    kind: InboxItemKind.WORKTREE_MARKED,
    category: InboxCategory.HOUSEKEEPING,
    itemId: `worktree_marked:${name}`,
    projectId: "proj-1",
    waitingSince: new Date(Date.now() - 3_600_000).toISOString(),
    payload: {
      case: "worktreeMarked",
      value: {
        worktreeId: `wt-${name}`,
        name,
        path: `/home/u/.reliant/worktrees/proj/${name}`,
        daemonId,
        daemonName: daemonId,
        markId: `m-${name}`,
        markedAt: new Date(Date.now() - 3_600_000).toISOString(),
        reclaimAt: new Date(Date.now() + 20 * 3_600_000).toISOString(),
        reason: "landed",
      },
    },
  });
}

const d1Storage = storageItem({ category: InboxCategory.HOUSEKEEPING });
const d2Storage = storageItem(
  { itemId: "storage:d-2:kept", category: InboxCategory.HOUSEKEEPING },
  { daemonId: "d-2", daemonName: "Cloud box" },
);

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.keepMarkedWorktree.mockResolvedValue({});
  mocks.dismissInboxItem.mockResolvedValue({});
  setTransport(
    mockTransport({
      "InboxService.ListInbox": (req) => mocks.listInbox(req),
      "InboxService.KeepMarkedWorktree": (req) => mocks.keepMarkedWorktree(req),
      "InboxService.DismissInboxItem": (req) => mocks.dismissInboxItem(req),
    }),
  );
});

afterEach(() => setTransport(null));

function respond(items: InboxItem[]) {
  mocks.listInbox.mockResolvedValue(create(ListInboxResponseSchema, { items }));
}

describe("MachineHousekeepingCard", () => {
  it("lists only this machine's housekeeping, from a read of every housekeeping and alert item", async () => {
    // An approval is not housekeeping (a server that predates `categories`
    // answers with the whole Inbox; the card files by kind and machine).
    respond([approvalItem(), d1Storage, d2Storage, markedItem("d-1", "fix-login"), markedItem("d-2", "spike")]);
    renderInboxAt(<MachineHousekeepingCard daemonId="d-1" />);

    const card = await screen.findByTestId("machine-housekeeping-card");
    expect(within(card).getByTestId(`inbox-item-${d1Storage.itemId}`)).toBeInTheDocument();
    expect(within(card).getByTestId("inbox-item-worktree_marked:fix-login")).toBeInTheDocument();
    expect(within(card).queryByTestId(`inbox-item-${d2Storage.itemId}`)).toBeNull();
    expect(within(card).queryByTestId("inbox-item-worktree_marked:spike")).toBeNull();
    expect(within(card).queryByTestId("inbox-item-approval:appr-1")).toBeNull();

    expect(mocks.listInbox).toHaveBeenCalledTimes(1);
    expect(mocks.listInbox.mock.calls[0]![0].categories).toEqual([InboxCategory.HOUSEKEEPING]);
  });

  it("several machines share one read", async () => {
    respond([d1Storage, d2Storage]);
    renderInboxAt(
      <>
        <MachineHousekeepingCard daemonId="d-1" title="One" />
        <MachineHousekeepingCard daemonId="d-2" title="Two" />
      </>,
    );
    expect(await screen.findAllByTestId("machine-housekeeping-card")).toHaveLength(2);
    expect(mocks.listInbox).toHaveBeenCalledTimes(1);
  });

  it("the Inbox row's actions work here: Keep a marked worktree, dismiss with Undo", async () => {
    respond([markedItem("d-1", "fix-login")]);
    renderInboxAt(<MachineHousekeepingCard daemonId="d-1" />);
    const row = await screen.findByTestId("inbox-item-worktree_marked:fix-login");

    await userEvent.click(within(row).getByRole("button", { name: "Keep" }));
    await waitFor(() =>
      expect(mocks.keepMarkedWorktree.mock.calls[0]![0]).toMatchObject({ worktreeId: "wt-fix-login", markId: "m-fix-login" }),
    );

    await userEvent.click(within(row).getByRole("button", { name: "Dismiss fix-login" }));
    await waitFor(() => expect(mocks.dismissInboxItem.mock.calls[0]![0]).toMatchObject({ itemIds: ["worktree_marked:fix-login"] }));
    await waitFor(() => expect(mocks.notify).toHaveBeenCalledWith("Dismissed “fix-login”", expect.anything()));
  });

  it("nothing waiting on this machine: no card", async () => {
    respond([d2Storage]);
    renderInboxAt(
      <>
        <MachineHousekeepingCard daemonId="d-1" title="One" />
        <MachineHousekeepingCard daemonId="d-2" title="Two" />
      </>,
    );
    // The read has landed (d-2's card shows), and d-1 has no card.
    expect(await screen.findByText("Two")).toBeInTheDocument();
    expect(screen.getAllByTestId("machine-housekeeping-card")).toHaveLength(1);
    expect(screen.queryByText("One")).toBeNull();
  });
});
