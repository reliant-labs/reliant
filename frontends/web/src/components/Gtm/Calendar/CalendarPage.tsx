// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Calendar: what goes out when (ListItems, by scheduled_at), by month
 * or week, each item tinted by its channel. Drag an item (or press M on it)
 * to another day to reschedule it: UpdateItem{scheduled_at} on the revision
 * on screen. A date records when it goes out and never publishes. What is
 * already out is pinned. On a phone the grid is narrow, so a list of what is
 * coming up follows it. An item opens the shared drawer, whose claim check
 * is computed live from its body.
 */

import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState } from "react";

import Calendar, { type CalendarItem, type CalendarView } from "@/components/forge-ui/calendar";
import EmptyState from "@/components/forge-ui/empty_state";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { GtmItemKind, type ListItemsResponse } from "@/gen/reliant/v1/gtm_pb";
import { gtmServiceKeys, useListItems, useUpdateItem } from "@/hooks/gtm-service-hooks_gen";

import { GtmShell, useGtmOrgId } from "../shared/GtmShell";
import { ItemDialog } from "../shared/ItemDialog";
import { canEdit } from "../shared/lifecycle";
import { CHANNELS, formatWhen, ItemStatusBadge, kindAndChannel, tsDate, tsMs } from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";

/** The most items the calendar reads; the list's own page limit (ListItemsRequest.page_size). */
const ITEMS_PAGE = 500;

export function CalendarPage() {
  return (
    <GtmShell page="calendar">
      <GtmCalendarView />
    </GtmShell>
  );
}

export function GtmCalendarView() {
  const orgId = useGtmOrgId();
  const itemsQ = useListItems({ orgId, pageSize: ITEMS_PAGE }, { enabled: !!orgId });
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const view: CalendarView = search.view === "week" ? "week" : "month";
  const setView = (next: CalendarView) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, view: next === "month" ? undefined : next }) } as never);
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

  const items = useMemo(() => itemsQ.data?.items ?? [], [itemsQ.data]);
  const calendarItems = useMemo<CalendarItem[]>(
    () =>
      items
        .filter((i) => i.scheduledAt !== undefined)
        .map((i) => ({
          id: i.id,
          date: tsDate(i.scheduledAt)!,
          title: i.kind === GtmItemKind.EMAIL ? `Email: ${i.title}` : i.title,
          channel: i.channel,
          movable: canEdit(i),
        })),
    [items],
  );
  const upcoming = useMemo(() => {
    const start = new Date();
    start.setHours(0, 0, 0, 0);
    return items.filter((i) => i.scheduledAt && tsMs(i.scheduledAt) >= start.getTime()).sort((a, b) => tsMs(a.scheduledAt) - tsMs(b.scheduledAt));
  }, [items]);

  function onMove(id: string, date: Date) {
    const item = items.find((i) => i.id === id);
    if (!item || !canEdit(item)) return;
    reschedule.mutate({ orgId, id, expectedRevision: item.revision, scheduledAt: timestampFromDate(date) });
  }

  const unscheduled = items.length - calendarItems.length;

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
      {itemsQ.isPending ? (
        <SkeletonLoader variant="rectangular" height="480px" />
      ) : itemsQ.isError ? (
        <EmptyState title="Couldn't load the calendar" description="Something went wrong reading this org's posts and emails." actionLabel="Try again" onAction={() => void itemsQ.refetch()} />
      ) : (
        <>
          <div className="overflow-x-auto" data-testid="gtm-calendar">
            <Calendar
              items={calendarItems}
              channels={CHANNELS}
              view={view}
              onViewChange={setView}
              onMove={onMove}
              onItemClick={(item) => setOpenId(item.id)}
              maxItemsPerDay={3}
              className="min-w-[340px]"
            />
          </div>
          <p className="flex flex-wrap gap-x-3 text-xs text-ink-muted">
            {unscheduled > 0 && <span>{unscheduled === 1 ? "1 item has" : `${unscheduled} items have`} no date yet.</span>}
            {itemsQ.data?.nextPageToken ? <span data-testid="gtm-calendar-truncated">Showing the first {ITEMS_PAGE} items.</span> : null}
          </p>
          <section aria-labelledby="gtm-upcoming" className="md:hidden">
            <h2 id="gtm-upcoming" className="mb-2 text-sm font-medium text-ink">
              Coming up
            </h2>
            {upcoming.length === 0 ? (
              <p className="text-sm text-ink-muted">Nothing scheduled from today on.</p>
            ) : (
              <ul className="divide-y divide-border rounded-lg border border-border bg-surface">
                {upcoming.map((i) => (
                  <li key={i.id}>
                    <button type="button" onClick={() => setOpenId(i.id)} className="flex w-full flex-col gap-1 px-3 py-2.5 text-left hover:bg-ink/5">
                      <span className="text-sm font-medium text-ink">{i.title}</span>
                      <span className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
                        <ItemStatusBadge status={i.status} />
                        {formatWhen(i.scheduledAt)} · {kindAndChannel(i.kind, i.channel)}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </>
      )}
      <ItemDialog item={items.find((i) => i.id === openId) ?? null} onClose={() => setOpenId(null)} />
    </>
  );
}
