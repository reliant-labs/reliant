// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Calendar: what goes out when (ListItems, by scheduled_at), by month
 * or week, each item tinted by its channel, filtered by channel and format
 * (`?channel=`, `?format=`). Drag an item (or press M on it)
 * to another day to reschedule it: UpdateItem{scheduled_at} on the revision
 * on screen. A date records when it goes out and never publishes. What is
 * already out is pinned. An item opens the shared drawer, whose claim check
 * is computed live from its body.
 *
 * It reads only what the visible month or week holds (scheduled_from /
 * scheduled_to, the grid's whole weeks), so a busy org's older or later items
 * never crowd out the ones on screen. On a phone a month's grid is too narrow
 * for titles, so the same range reads as an agenda: day by day, each item's
 * time, title and channel.
 */

import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import Calendar, { type CalendarItem, type CalendarView } from "@/components/forge-ui/calendar";
import EmptyState from "@/components/forge-ui/empty_state";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { GtmItemFormat, type GtmChannel, type GtmItem, type ListItemsResponse } from "@/gen/reliant/v1/gtm_pb";
import { gtmServiceKeys, useListItems, useUpdateItem } from "@/hooks/gtm-service-hooks_gen";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { keepPreviousInOrg } from "@/lib/orgScope";

import { GtmShell, useGtmOrgId } from "../shared/GtmShell";
import { ItemDialog } from "../shared/ItemDialog";
import { canEdit } from "../shared/lifecycle";
import { useGtmChannels } from "../shared/ItemDialog";
import { channelColor, formatAndChannel, formatLabel, isEmailFormat, ITEM_FORMATS, ItemStatusBadge, tsDate, tsMs } from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";

const FILTER =
  "h-8 rounded-md border border-border-strong bg-surface px-2.5 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** The most items the calendar reads for one month or week; the list's own page limit (ListItemsRequest.page_size). */
const ITEMS_PAGE = 500;

/** The grid's first day of the week (forge-ui calendar's default: Sunday). */
const WEEK_STARTS_ON = 0;

const addDays = (d: Date, n: number) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
const startOfWeek = (d: Date) => addDays(d, -((d.getDay() - WEEK_STARTS_ON + 7) % 7));

/**
 * The days the grid shows for `anchor`, as a half-open range [from, to): the
 * week holding it, or every whole week the month touches. What ListItems is
 * asked for (scheduled_from <= scheduled_at < scheduled_to).
 */
export function visibleRange(anchor: Date, view: CalendarView): { from: Date; to: Date } {
  if (view === "week") {
    const from = startOfWeek(anchor);
    return { from, to: addDays(from, 7) };
  }
  const first = new Date(anchor.getFullYear(), anchor.getMonth(), 1);
  const last = new Date(anchor.getFullYear(), anchor.getMonth() + 1, 0);
  return { from: startOfWeek(first), to: addDays(startOfWeek(last), 7) };
}

const monthFmt = new Intl.DateTimeFormat(undefined, { month: "long", year: "numeric" });
const dayFmt = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric" });
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });

export function CalendarPage() {
  return (
    <GtmShell page="calendar">
      <GtmCalendarView />
    </GtmShell>
  );
}

export function GtmCalendarView() {
  const orgId = useGtmOrgId();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const view: CalendarView = search.view === "week" ? "week" : "month";
  const [anchor, setAnchor] = useState(() => new Date());
  const range = useMemo(() => visibleRange(anchor, view), [anchor, view]);
  const itemsQ = useListItems(
    { orgId, pageSize: ITEMS_PAGE, scheduledFrom: timestampFromDate(range.from), scheduledTo: timestampFromDate(range.to) },
    // The last range stays on screen while the next one is read.
    { enabled: !!orgId, placeholderData: keepPreviousInOrg(orgId) },
  );
  // A phone (a narrow screen) reads the range as an agenda: the grid has no room for titles.
  const narrow = useMediaQuery("(max-width: 767px)");
  const setView = (next: CalendarView) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, view: next === "month" ? undefined : next }) } as never);
  const channelFilter = typeof search.channel === "string" ? search.channel : "";
  const formatFilter = Number(search.format) || 0;
  const setFilter = (key: "channel" | "format", value: string) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, [key]: value || undefined }) } as never);
  const channels = useGtmChannels();
  const [openId, setOpenId] = useState<string | null>(null);
  const onError = useGtmWriteError();
  const queryClient = useQueryClient();
  // The chip moves at once (the cached lists show the new date); a refused
  // move puts it back, and the success invalidation reads the server's answer.
  const reschedule = useUpdateItem({
    onMutate: async (req) => {
      const lists = { queryKey: gtmServiceKeys.listItems.all };
      await queryClient.cancelQueries(lists);
      const before = queryClient.getQueriesData<ListItemsResponse>(lists);
      queryClient.setQueriesData<ListItemsResponse>(lists, (old) =>
        // A paged read under the same key holds pages, not items: left alone.
        old && Array.isArray(old.items) ? { ...old, items: old.items.map((i) => (i.id === req.id ? { ...i, scheduledAt: req.scheduledAt as typeof i.scheduledAt } : i)) } : old,
      );
      return { before };
    },
    onError: (error, _req, context) => {
      for (const [key, data] of context?.before ?? []) queryClient.setQueryData(key, data);
      onError(error);
    },
  });

  const allItems = useMemo(() => itemsQ.data?.items ?? [], [itemsQ.data]);
  const items = useMemo(
    () => allItems.filter((i) => (!channelFilter || i.channelId === channelFilter) && (!formatFilter || i.format === formatFilter)),
    [allItems, channelFilter, formatFilter],
  );
  // The legend names the channels something is scheduled on, coloured by channel.
  const calendarChannels = useMemo(
    () => channels.filter((c) => items.some((i) => i.channelId === c.id && i.scheduledAt)).map((c) => ({ id: c.id, label: c.name, color: channelColor(c, channels) })),
    [channels, items],
  );
  const calendarItems = useMemo<CalendarItem[]>(
    () =>
      items
        .filter((i) => i.scheduledAt !== undefined)
        .map((i) => ({
          id: i.id,
          date: tsDate(i.scheduledAt)!,
          title: isEmailFormat(i.format) ? `Email: ${i.title}` : i.title,
          channel: i.channelId || undefined,
          movable: canEdit(i),
        })),
    [items],
  );

  function onMove(id: string, date: Date) {
    const item = items.find((i) => i.id === id);
    if (!item || !canEdit(item)) return;
    reschedule.mutate({ orgId, id, expectedRevision: item.revision, scheduledAt: timestampFromDate(date) });
  }

  return (
    <>
      <PageHeader
        title="Calendar"
        meta={
          <>
            <span>What goes out when.</span>
            <span>Drag to reschedule. Scheduling never publishes.</span>
          </>
        }
        className=""
      />
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Filter the calendar">
        <select aria-label="Channel" className={FILTER} value={channelFilter} onChange={(e) => setFilter("channel", e.target.value)}>
          <option value="">All channels</option>
          {channels.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
        <select aria-label="Format" className={FILTER} value={formatFilter || ""} onChange={(e) => setFilter("format", e.target.value)}>
          <option value="">All formats</option>
          {ITEM_FORMATS.map((f: GtmItemFormat) => (
            <option key={f} value={f}>
              {formatLabel(f)}
            </option>
          ))}
        </select>
      </div>
      {itemsQ.isPending ? (
        <SkeletonLoader variant="rectangular" height="480px" />
      ) : itemsQ.isError ? (
        <EmptyState title="Couldn't load the calendar" description="Something went wrong reading this org's posts and emails." actionLabel="Try again" onAction={() => void itemsQ.refetch()} />
      ) : (
        <>
          {narrow ? (
            <Agenda
              items={items}
              channels={channels}
              anchor={anchor}
              view={view}
              range={range}
              onAnchor={setAnchor}
              onView={setView}
              onOpen={setOpenId}
            />
          ) : (
            <div className="overflow-x-auto" data-testid="gtm-calendar-grid">
              <Calendar
                items={calendarItems}
                channels={calendarChannels}
                view={view}
                onViewChange={setView}
                date={anchor}
                onDateChange={setAnchor}
                weekStartsOn={WEEK_STARTS_ON}
                onMove={onMove}
                onItemClick={(item) => setOpenId(item.id)}
                maxItemsPerDay={3}
                className="min-w-[340px]"
              />
            </div>
          )}
          {itemsQ.data?.nextPageToken ? (
            <p className="text-xs text-ink-muted" data-testid="gtm-calendar-truncated">
              Showing the first {ITEMS_PAGE} items in this {view}.
            </p>
          ) : null}
        </>
      )}
      <ItemDialog item={items.find((i) => i.id === openId) ?? null} onClose={() => setOpenId(null)} />
    </>
  );
}

/**
 * The visible month or week as a list, for a phone: each day with something
 * on it, and each item's time, title, status and channel. Its own month or
 * week switch and previous / next, since the grid's are not drawn.
 */
function Agenda({
  items,
  channels,
  anchor,
  view,
  range,
  onAnchor,
  onView,
  onOpen,
}: {
  items: GtmItem[];
  channels: GtmChannel[];
  anchor: Date;
  view: CalendarView;
  range: { from: Date; to: Date };
  onAnchor: (d: Date) => void;
  onView: (v: CalendarView) => void;
  onOpen: (id: string) => void;
}) {
  const days = useMemo(() => {
    const byDay = new Map<string, { day: Date; items: GtmItem[] }>();
    const inView = (d: Date) => (view === "month" ? d.getMonth() === anchor.getMonth() : d >= range.from && d < range.to);
    for (const i of [...items].filter((i) => i.scheduledAt).sort((a, b) => tsMs(a.scheduledAt) - tsMs(b.scheduledAt))) {
      const at = tsDate(i.scheduledAt)!;
      if (!inView(at)) continue;
      const key = `${at.getFullYear()}-${at.getMonth()}-${at.getDate()}`;
      const entry = byDay.get(key) ?? { day: new Date(at.getFullYear(), at.getMonth(), at.getDate()), items: [] };
      entry.items.push(i);
      byDay.set(key, entry);
    }
    return [...byDay.values()];
  }, [items, anchor, view, range]);
  const step = (n: number) => onAnchor(view === "week" ? addDays(anchor, 7 * n) : new Date(anchor.getFullYear(), anchor.getMonth() + n, 1));
  const label = view === "week" ? `${dayFmt.format(range.from)} – ${dayFmt.format(addDays(range.to, -1))}` : monthFmt.format(anchor);
  return (
    <section aria-label={`Agenda, ${label}`} className="space-y-3" data-testid="gtm-calendar-agenda">
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" onClick={() => onAnchor(new Date())}>
          Today
        </Button>
        <Button variant="outline" size="sm" aria-label={view === "week" ? "Previous week" : "Previous month"} onClick={() => step(-1)}>
          ‹
        </Button>
        <Button variant="outline" size="sm" aria-label={view === "week" ? "Next week" : "Next month"} onClick={() => step(1)}>
          ›
        </Button>
        <h2 className="text-sm font-semibold text-ink" aria-live="polite">
          {label}
        </h2>
        <div className="ml-auto flex gap-1" role="group" aria-label="View">
          {(["month", "week"] as const).map((v) => (
            <Button key={v} variant={v === view ? "secondary" : "ghost"} size="sm" aria-pressed={v === view} onClick={() => onView(v)}>
              {v === "month" ? "Month" : "Week"}
            </Button>
          ))}
        </div>
      </div>
      {days.length === 0 ? (
        <p className="text-sm text-ink-muted">Nothing scheduled this {view}.</p>
      ) : (
        <ol className="space-y-4">
          {days.map(({ day, items: dayItems }) => (
            <li key={day.toISOString()}>
              <h3 className="mb-1.5 text-xs font-medium text-ink-muted">{dayFmt.format(day)}</h3>
              <ul className="divide-y divide-border rounded-lg border border-border bg-surface">
                {dayItems.map((i) => {
                  const channel = channels.find((c) => c.id === i.channelId);
                  return (
                    <li key={i.id}>
                      <button type="button" onClick={() => onOpen(i.id)} className="flex w-full items-start gap-3 px-3 py-2.5 text-left hover:bg-ink/5">
                        <span
                          aria-hidden="true"
                          className="mt-1.5 size-2 shrink-0 rounded-full"
                          style={{ background: channel ? channelColor(channel, channels) : "transparent" }}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="block text-sm font-medium text-ink">{isEmailFormat(i.format) ? `Email: ${i.title}` : i.title}</span>
                          <span className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-ink-muted">
                            <span className="tabular-nums">{timeFmt.format(tsDate(i.scheduledAt)!)}</span>
                            <ItemStatusBadge status={i.status} />
                            {formatAndChannel(i.format, channel)}
                          </span>
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
