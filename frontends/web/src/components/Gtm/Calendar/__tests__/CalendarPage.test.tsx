// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Calendar over GtmService (a mock transport): what has a date is on
 * the calendar, a drag is UpdateItem{scheduled_at} on the revision on screen,
 * what is out is pinned, and an item opens the drawer whose claim check runs
 * live on its body.
 */

import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { GtmItemKind, GtmItemSchema, GtmItemStatus, type UpdateItemRequest } from "@/gen/reliant/v1/gtm_pb";

import { renderGtm, TEST_ORG } from "../../shared/__tests__/gtmHarness";
import { GtmCalendarView } from "../CalendarPage";

vi.mock("@/lib/toast-manager", () => ({ toast: { warning: vi.fn(), error: vi.fn(), info: vi.fn() } }));

const NEW_DAY = new Date(2026, 10, 20, 9, 0);

// The grid, drag and keyboard moves are forge-ui calendar's own; a stand-in
// draws each item and reports a move the way it does (onMove(id, date)).
vi.mock("@/components/forge-ui/calendar", () => ({
  default: ({
    items,
    onMove,
    onItemClick,
  }: {
    items: { id: string; title: string; movable?: boolean }[];
    onMove: (id: string, date: Date) => void;
    onItemClick: (item: { id: string }) => void;
  }) => (
    <ul>
      {items.map((item) => (
        <li key={item.id} data-movable={String(item.movable !== false)} data-testid={`chip-${item.id}`}>
          <button type="button" onClick={() => onItemClick(item)}>
            {item.title}
          </button>
          <button type="button" onClick={() => onMove(item.id, NEW_DAY)}>{`move ${item.title}`}</button>
        </li>
      ))}
    </ul>
  ),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const when = timestampFromDate(new Date(2026, 10, 18, 9, 0));
const post = create(GtmItemSchema, { id: "item-1", orgId: TEST_ORG, kind: GtmItemKind.SOCIAL_POST, channel: "x", title: "Show HN post", bodyMd: "Deploy in one command.", status: GtmItemStatus.REVIEW, scheduledAt: when, revision: 4 });
const sent = create(GtmItemSchema, { id: "item-2", orgId: TEST_ORG, kind: GtmItemKind.EMAIL, channel: "email", title: "Invite", status: GtmItemStatus.SENT, scheduledAt: when, revision: 9 });
const undated = create(GtmItemSchema, { id: "item-3", orgId: TEST_ORG, kind: GtmItemKind.BLOG_POST, title: "Someday post", status: GtmItemStatus.DRAFT, revision: 1 });

const reads = {
  "GtmService.ListItems": { response: { items: [post, sent, undated] } },
  "GtmService.ListCampaigns": { response: { campaigns: [] } },
  "GtmService.CheckClaims": { response: { findings: [] } },
};

describe("GtmCalendarView", () => {
  it("draws what has a date, pins what is out, and counts the undated", async () => {
    renderGtm(<GtmCalendarView />, reads, { path: "/gtm/calendar" });
    expect(await screen.findByRole("button", { name: "Show HN post" })).toBeInTheDocument();
    expect(screen.getByTestId("chip-item-1")).toHaveAttribute("data-movable", "true");
    expect(screen.getByTestId("chip-item-2")).toHaveAttribute("data-movable", "false");
    expect(screen.getByRole("button", { name: "Email: Invite" })).toBeInTheDocument();
    expect(screen.queryByText("Someday post")).not.toBeInTheDocument();
    expect(screen.getByText("1 item has no date yet.")).toBeInTheDocument();
  });

  it("reschedules on a drag, on the revision on screen", async () => {
    const update = vi.fn((req: unknown) => ({ item: { ...post, scheduledAt: (req as UpdateItemRequest).scheduledAt } }));
    renderGtm(<GtmCalendarView />, { ...reads, "GtmService.UpdateItem": update }, { path: "/gtm/calendar" });
    fireEvent.click(await screen.findByRole("button", { name: "move Show HN post" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    const req = update.mock.calls[0]![0] as UpdateItemRequest;
    expect(req).toEqual(expect.objectContaining({ orgId: TEST_ORG, id: "item-1", expectedRevision: 4 }));
    expect(timestampDate(req.scheduledAt!).getTime()).toBe(NEW_DAY.getTime());
    expect(req.status).toBeUndefined();
  });

  it("does not move what is out", async () => {
    const update = vi.fn(() => ({}));
    renderGtm(<GtmCalendarView />, { ...reads, "GtmService.UpdateItem": update }, { path: "/gtm/calendar" });
    fireEvent.click(await screen.findByRole("button", { name: "move Email: Invite" }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(update).not.toHaveBeenCalled();
  });

  it("opens an item with its claim check run live on the body", async () => {
    const check = vi.fn(() => ({ findings: [] }));
    renderGtm(<GtmCalendarView />, { ...reads, "GtmService.CheckClaims": check }, { path: "/gtm/calendar" });
    fireEvent.click(await screen.findByRole("button", { name: "Show HN post" }));
    expect(await screen.findByTestId("gtm-item-dialog")).toBeInTheDocument();
    await waitFor(() => expect(check).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, text: "Deploy in one command." })));
  });
});
