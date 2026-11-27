// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Calendar over GtmService (a mock transport): what has a date is on
 * the calendar, a drag is UpdateItem{scheduled_at} on the revision on screen,
 * what is out is pinned, and an item opens the drawer whose claim check runs
 * live on its body.
 */

import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { GtmChannelMode, GtmChannelSchema, GtmItemFormat, GtmItemSchema, GtmItemStatus, type ListItemsRequest, type UpdateItemRequest } from "@/gen/reliant/v1/gtm_pb";

import { renderGtm, TEST_ORG } from "../../shared/__tests__/gtmHarness";
import { GtmCalendarView, visibleRange } from "../CalendarPage";

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
const post = create(GtmItemSchema, { id: "item-1", orgId: TEST_ORG, format: GtmItemFormat.SHORT_POST, channelId: "ch-x", title: "Show HN post", bodyMd: "Deploy in one command.", status: GtmItemStatus.REVIEW, scheduledAt: when, revision: 4 });
const sent = create(GtmItemSchema, { id: "item-2", orgId: TEST_ORG, format: GtmItemFormat.EMAIL, channelId: "ch-email", title: "Invite", status: GtmItemStatus.SENT, scheduledAt: when, revision: 9 });
const undated = create(GtmItemSchema, { id: "item-3", orgId: TEST_ORG, format: GtmItemFormat.LONG_POST, title: "Someday post", status: GtmItemStatus.DRAFT, revision: 1 });

const xChannel = create(GtmChannelSchema, { id: "ch-x", orgId: TEST_ORG, key: "x", name: "X", mode: GtmChannelMode.MANUAL });
const emailChannel = create(GtmChannelSchema, { id: "ch-email", orgId: TEST_ORG, key: "email", name: "Email", mode: GtmChannelMode.INTEGRATED, integrationKind: "gmail" });

const reads = {
  "GtmService.ListChannels": { response: { channels: [xChannel, emailChannel] } },
  "GtmService.ListAssets": { response: { assets: [] } },
  "GtmService.ListItems": { response: { items: [post, sent, undated] } },
  "GtmService.ListCampaigns": { response: { campaigns: [] } },
  "GtmService.CheckClaims": { response: { findings: [] } },
};

describe("GtmCalendarView", () => {
  it("draws what has a date and pins what is out", async () => {
    renderGtm(<GtmCalendarView />, reads, { path: "/gtm/calendar" });
    expect(await screen.findByRole("button", { name: "Show HN post" })).toBeInTheDocument();
    expect(screen.getByTestId("chip-item-1")).toHaveAttribute("data-movable", "true");
    expect(screen.getByTestId("chip-item-2")).toHaveAttribute("data-movable", "false");
    expect(screen.getByRole("button", { name: "Email: Invite" })).toBeInTheDocument();
    expect(screen.queryByText("Someday post")).not.toBeInTheDocument();
    // One element per test id: the page's own wrapper is gtm-calendar (GtmShell), the grid gtm-calendar-grid.
    expect(screen.getAllByTestId("gtm-calendar-grid")).toHaveLength(1);
  });

  it("asks only for the visible month's whole weeks, then the week's", async () => {
    const list = vi.fn(() => ({ items: [post] }));
    renderGtm(<GtmCalendarView />, { ...reads, "GtmService.ListItems": list }, { path: "/gtm/calendar" });
    await screen.findByRole("button", { name: "Show HN post" });
    const req = (list.mock.calls[0] as unknown[])[0] as ListItemsRequest;
    const { from, to } = visibleRange(new Date(), "month");
    expect(timestampDate(req.scheduledFrom!).getTime()).toBe(from.getTime());
    expect(timestampDate(req.scheduledTo!).getTime()).toBe(to.getTime());
    expect(from.getDay()).toBe(0);
  });

  it("knows a month's grid and a week's range", () => {
    // November 2026 starts on a Sunday and ends on a Monday: five weeks, Nov 1 – Dec 6.
    expect(visibleRange(new Date(2026, 10, 18), "month")).toEqual({ from: new Date(2026, 10, 1), to: new Date(2026, 11, 6) });
    expect(visibleRange(new Date(2026, 10, 18), "week")).toEqual({ from: new Date(2026, 10, 15), to: new Date(2026, 10, 22) });
  });

  it("reads as an agenda on a phone: each day with its items' times, titles and channels", async () => {
    const realMatchMedia = window.matchMedia;
    window.matchMedia = ((query: string) => ({ matches: query.includes("max-width"), media: query, addEventListener: () => {}, removeEventListener: () => {} })) as never;
    try {
      const today = new Date();
      const onDay = (d: number, h: number) => timestampFromDate(new Date(today.getFullYear(), today.getMonth(), d, h, 0));
      const a = create(GtmItemSchema, { ...post, scheduledAt: onDay(3, 9) });
      const b = create(GtmItemSchema, { ...sent, scheduledAt: onDay(3, 14) });
      renderGtm(<GtmCalendarView />, { ...reads, "GtmService.ListItems": { response: { items: [a, b] } } }, { path: "/gtm/calendar" });
      const agenda = await screen.findByTestId("gtm-calendar-agenda");
      expect(screen.queryByTestId("gtm-calendar-grid")).not.toBeInTheDocument();
      expect(within(agenda).getByRole("button", { name: /Show HN post/ })).toBeInTheDocument();
      expect(within(agenda).getByRole("button", { name: /Email: Invite/ })).toBeInTheDocument();
      expect(within(agenda).getAllByRole("heading", { level: 3 })).toHaveLength(1);
      fireEvent.click(within(agenda).getByRole("button", { name: /Show HN post/ }));
      expect(await screen.findByTestId("gtm-item-dialog")).toBeInTheDocument();
    } finally {
      window.matchMedia = realMatchMedia;
    }
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

  it("filters by channel and by format", async () => {
    renderGtm(<GtmCalendarView />, reads, { path: "/gtm/calendar?channel=ch-x", routePath: "/gtm/calendar" });
    expect(await screen.findByRole("button", { name: "Show HN post" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Email: Invite" })).not.toBeInTheDocument();
    cleanup();
    renderGtm(<GtmCalendarView />, reads, { path: `/gtm/calendar?format=${GtmItemFormat.EMAIL}`, routePath: "/gtm/calendar" });
    expect(await screen.findByRole("button", { name: "Email: Invite" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show HN post" })).not.toBeInTheDocument();
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
