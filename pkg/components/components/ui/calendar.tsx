import React from "react";

/**
 * Calendar — month and week views of dated items (scheduled posts, launches,
 * deadlines), each a chip tinted by its channel. Drag a chip to another day
 * to reschedule it; click a day's empty space to create something there.
 *
 *   <Calendar
 *     items={posts.map((p) => ({ id: p.id, date: p.scheduledAt, title: p.title, channel: p.channel }))}
 *     channels={[
 *       { id: "bluesky", label: "Bluesky", color: "#1185fe" },
 *       { id: "hn", label: "Hacker News", color: "#ff6600" },
 *     ]}
 *     onMove={(id, date) => reschedule.mutate({ id, date })}
 *     onDayClick={(date) => openComposer(date)}
 *     onItemClick={(item) => openPost(item.id)}
 *   />
 *
 * `onMove` receives the new day with the item's original time of day kept,
 * so a 09:30 post dragged to Thursday is a 09:30 post on Thursday. The
 * calendar never moves an item itself: render the new date once it is saved.
 *
 * Dates are local. A date-only string ("2026-11-12") is that day in the
 * viewer's time zone, not UTC midnight.
 *
 * Keyboard: the grid is one tab stop. Arrows move a day / a week, Home/End
 * go to the start/end of the week, PageUp/PageDown change month (week in
 * week view), Enter or Space on a day calls `onDayClick`. Tab from a day
 * reaches its items; on an item, M picks it up, arrows choose the day,
 * Enter drops it (`onMove`) and Escape cancels.
 *
 * `view` and `date` (the day the visible month or week contains) are each
 * controlled when passed and uncontrolled otherwise.
 */
export interface CalendarChannel {
  id: string;
  label: string;
  /** Any CSS colour. Defaults to a fixed categorical palette, by position. */
  color?: string;
}

export interface CalendarItem {
  id: string;
  /** A Date, an ISO timestamp, or a date-only "YYYY-MM-DD" (local). */
  date: Date | string;
  title: string;
  /** A CalendarChannel id; picks the chip colour. */
  channel?: string;
  /** Overrides the time shown before the title. Untimed items show none. */
  timeLabel?: string;
  /** Set false to pin an item (already published, say) in place. */
  movable?: boolean;
}

export type CalendarView = "month" | "week";

export interface CalendarProps {
  items: CalendarItem[];
  channels?: CalendarChannel[];
  /** Controlled view. */
  view?: CalendarView;
  /** Initial view when uncontrolled. Defaults to "month". */
  defaultView?: CalendarView;
  onViewChange?: (view: CalendarView) => void;
  /** Controlled anchor: any day inside the month or week to show. */
  date?: Date;
  /** Initial anchor when uncontrolled. Defaults to today. */
  defaultDate?: Date;
  onDateChange?: (date: Date) => void;
  /** Reschedule. Without it, items cannot be dragged. */
  onMove?: (itemId: string, newDate: Date) => void;
  /** A click (or Enter) on a day's empty space, not on an item. */
  onDayClick?: (date: Date) => void;
  onItemClick?: (item: CalendarItem) => void;
  /** 0 = Sunday (default), 1 = Monday. */
  weekStartsOn?: 0 | 1;
  /** BCP 47 locale for month and weekday names. Defaults to the browser's. */
  locale?: string;
  /** Chips shown per day in month view before "+N more". Defaults to 3. */
  maxItemsPerDay?: number;
  /** Which day is today. Pass it when rendering on a server in another zone. */
  today?: Date;
  /** Accessible name of the grid. Defaults to the visible month or week. */
  ariaLabel?: string;
  className?: string;
}

const palette = [
  "#6366f1",
  "#0ea5e9",
  "#10b981",
  "#f59e0b",
  "#ef4444",
  "#8b5cf6",
  "#ec4899",
  "#14b8a6",
];
const itemMime = "application/x-calendar-item";

// ── Local-date arithmetic ──────────────────────────────────────────────

const pad = (n: number) => String(n).padStart(2, "0");
const dayKey = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate());

function toDate(v: Date | string): Date {
  if (v instanceof Date) return v;
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v);
  if (m) return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return new Date(v);
}

function isUntimed(v: Date | string): boolean {
  return typeof v === "string" && /^\d{4}-\d{2}-\d{2}$/.test(v);
}

function addDays(d: Date, n: number): Date {
  const r = new Date(d);
  r.setDate(r.getDate() + n);
  return r;
}

function addMonths(d: Date, n: number): Date {
  const first = new Date(d.getFullYear(), d.getMonth() + n, 1);
  const last = new Date(first.getFullYear(), first.getMonth() + 1, 0).getDate();
  return new Date(first.getFullYear(), first.getMonth(), Math.min(d.getDate(), last));
}

function startOfWeek(d: Date, weekStartsOn: number): Date {
  const s = startOfDay(d);
  return addDays(s, -((s.getDay() - weekStartsOn + 7) % 7));
}

/** The visible days, as rows of seven. */
function visibleWeeks(anchor: Date, view: CalendarView, weekStartsOn: number): Date[][] {
  const start =
    view === "week"
      ? startOfWeek(anchor, weekStartsOn)
      : startOfWeek(new Date(anchor.getFullYear(), anchor.getMonth(), 1), weekStartsOn);
  const lastOfMonth = new Date(anchor.getFullYear(), anchor.getMonth() + 1, 0);
  const weeks: Date[][] = [];
  let cursor = start;
  do {
    const week: Date[] = [];
    for (let i = 0; i < 7; i++) week.push(addDays(cursor, i));
    weeks.push(week);
    cursor = addDays(cursor, 7);
  } while (view === "month" && cursor <= lastOfMonth);
  return weeks;
}

/** The item's time of day carried onto a new day. */
function moveKeepingTime(original: Date, day: Date): Date {
  const next = startOfDay(day);
  next.setHours(
    original.getHours(),
    original.getMinutes(),
    original.getSeconds(),
    original.getMilliseconds(),
  );
  return next;
}

// ── Component ──────────────────────────────────────────────────────────

interface Grab {
  itemId: string;
  targetKey: string;
}

export default function Calendar({
  items,
  channels = [],
  view,
  defaultView = "month",
  onViewChange,
  date,
  defaultDate,
  onDateChange,
  onMove,
  onDayClick,
  onItemClick,
  weekStartsOn = 0,
  locale,
  maxItemsPerDay = 3,
  today,
  ariaLabel,
  className,
}: CalendarProps) {
  const [now] = React.useState(() => new Date());
  const todayKey = dayKey(today ?? now);

  const [uncontrolledView, setUncontrolledView] = React.useState<CalendarView>(defaultView);
  const viewControlled = view !== undefined;
  const activeView = view ?? uncontrolledView;

  const [uncontrolledDate, setUncontrolledDate] = React.useState<Date>(
    () => defaultDate ?? today ?? now,
  );
  const dateControlled = date !== undefined;
  const anchor = date ?? uncontrolledDate;

  const [focusedKey, setFocusedKey] = React.useState<string | null>(null);
  const [dragId, setDragId] = React.useState<string | null>(null);
  const [dropKey, setDropKey] = React.useState<string | null>(null);
  const [grab, setGrab] = React.useState<Grab | null>(null);
  const [announcement, setAnnouncement] = React.useState("");

  const gridRef = React.useRef<HTMLTableElement>(null);
  // Set by keyboard navigation so focus follows the roving tab stop after the
  // next render, and only then — a re-render from new props never steals focus.
  const focusAfterRender = React.useRef(false);
  const hintId = React.useId();

  const weeks = React.useMemo(
    () => visibleWeeks(anchor, activeView, weekStartsOn),
    [anchor, activeView, weekStartsOn],
  );
  const days = weeks.flat();
  const visibleKeys = days.map(dayKey);

  const colors = React.useMemo(() => {
    const map = new Map<string, CalendarChannel & { color: string }>();
    channels.forEach((c, i) =>
      map.set(c.id, { ...c, color: c.color ?? palette[i % palette.length] ?? "#6366f1" }),
    );
    return map;
  }, [channels]);

  const byDay = React.useMemo(() => {
    const map = new Map<string, Array<{ item: CalendarItem; at: Date }>>();
    for (const item of items) {
      const at = toDate(item.date);
      if (Number.isNaN(at.getTime())) continue;
      const key = dayKey(at);
      const list = map.get(key) ?? [];
      list.push({ item, at });
      map.set(key, list);
    }
    for (const list of map.values()) list.sort((a, b) => a.at.getTime() - b.at.getTime());
    return map;
  }, [items]);

  const firstOfMonthKey = dayKey(new Date(anchor.getFullYear(), anchor.getMonth(), 1));
  const fallbackKey = visibleKeys.includes(todayKey)
    ? todayKey
    : activeView === "month"
      ? firstOfMonthKey
      : (visibleKeys[0] ?? todayKey);
  const activeKey = focusedKey && visibleKeys.includes(focusedKey) ? focusedKey : fallbackKey;

  React.useEffect(() => {
    if (!focusAfterRender.current) return;
    focusAfterRender.current = false;
    gridRef.current?.querySelector<HTMLElement>(`[data-day="${activeKey}"]`)?.focus();
  }, [activeKey]);

  const fmt = React.useMemo(
    () => ({
      month: new Intl.DateTimeFormat(locale, { month: "long", year: "numeric" }),
      short: new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" }),
      shortYear: new Intl.DateTimeFormat(locale, {
        month: "short",
        day: "numeric",
        year: "numeric",
      }),
      long: new Intl.DateTimeFormat(locale, {
        weekday: "long",
        month: "long",
        day: "numeric",
        year: "numeric",
      }),
      weekday: new Intl.DateTimeFormat(locale, { weekday: "short" }),
      time: new Intl.DateTimeFormat(locale, { hour: "numeric", minute: "2-digit" }),
    }),
    [locale],
  );

  function setAnchor(next: Date) {
    if (!dateControlled) setUncontrolledDate(next);
    onDateChange?.(next);
  }

  function setView(next: CalendarView) {
    if (!viewControlled) setUncontrolledView(next);
    onViewChange?.(next);
  }

  function step(direction: 1 | -1) {
    setAnchor(
      activeView === "month" ? addMonths(anchor, direction) : addDays(anchor, 7 * direction),
    );
  }

  /** Move the roving focus to a day, paging the view when it leaves it. */
  function focusDay(day: Date) {
    const key = dayKey(day);
    focusAfterRender.current = true;
    setFocusedKey(key);
    const inView =
      activeView === "month"
        ? day.getMonth() === anchor.getMonth() && day.getFullYear() === anchor.getFullYear()
        : visibleKeys.includes(key);
    if (!inView) setAnchor(day);
  }

  function handleCellKeyDown(e: React.KeyboardEvent<HTMLTableCellElement>, day: Date) {
    let next: Date | null = null;
    switch (e.key) {
      case "ArrowLeft":
        next = addDays(day, -1);
        break;
      case "ArrowRight":
        next = addDays(day, 1);
        break;
      case "ArrowUp":
        next = addDays(day, -7);
        break;
      case "ArrowDown":
        next = addDays(day, 7);
        break;
      case "Home":
        next = startOfWeek(day, weekStartsOn);
        break;
      case "End":
        next = addDays(startOfWeek(day, weekStartsOn), 6);
        break;
      case "PageUp":
        next = activeView === "month" ? addMonths(day, -1) : addDays(day, -7);
        break;
      case "PageDown":
        next = activeView === "month" ? addMonths(day, 1) : addDays(day, 7);
        break;
      case "Enter":
      case " ":
        if (e.target === e.currentTarget && onDayClick) {
          e.preventDefault();
          onDayClick(startOfDay(day));
        }
        return;
      default:
        return;
    }
    e.preventDefault();
    focusDay(next);
  }

  function handleCellClick(e: React.MouseEvent<HTMLTableCellElement>, day: Date) {
    setFocusedKey(dayKey(day));
    if ((e.target as HTMLElement).closest("button")) return;
    onDayClick?.(startOfDay(day));
  }

  function commitMove(itemId: string, key: string) {
    const entry = items.find((i) => i.id === itemId);
    // The one guard every move path shares: a drop carries only an id, so a
    // pinned item must be refused here, not just made undraggable.
    if (!entry || !onMove || entry.movable === false) return;
    const from = toDate(entry.date);
    if (dayKey(from) === key) return;
    const target = toDate(key);
    onMove(itemId, moveKeepingTime(from, target));
    setAnnouncement(`Moved ${entry.title} to ${fmt.long.format(target)}`);
  }

  function handleItemKeyDown(
    e: React.KeyboardEvent<HTMLButtonElement>,
    item: CalendarItem,
    day: Date,
  ) {
    const movable = Boolean(onMove) && item.movable !== false;
    if (!grab) {
      if (movable && e.key.toLowerCase() === "m" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        e.stopPropagation();
        setGrab({ itemId: item.id, targetKey: dayKey(day) });
        setAnnouncement(
          `Picked up ${item.title}. Arrow keys choose a day, Enter drops it, Escape cancels.`,
        );
      }
      return; // arrows bubble to the day cell: grid navigation
    }
    if (grab.itemId !== item.id) return;
    e.stopPropagation();
    const target = toDate(grab.targetKey);
    let next: Date | null = null;
    switch (e.key) {
      case "ArrowLeft":
        next = addDays(target, -1);
        break;
      case "ArrowRight":
        next = addDays(target, 1);
        break;
      case "ArrowUp":
        next = addDays(target, -7);
        break;
      case "ArrowDown":
        next = addDays(target, 7);
        break;
      case "Enter":
        // Enter only: Space would also fire the button's click on keyup.
        e.preventDefault();
        setGrab(null);
        focusAfterRender.current = true;
        setFocusedKey(grab.targetKey);
        commitMove(item.id, grab.targetKey);
        return;
      case "Escape":
        e.preventDefault();
        setGrab(null);
        setAnnouncement(`Cancelled moving ${item.title}`);
        return;
      case "Tab":
        setGrab(null);
        return;
      default:
        return;
    }
    e.preventDefault();
    // Keyboard moves stay inside the visible range, so the item being moved
    // (and the focus on it) never scrolls out of view mid-move.
    const key = dayKey(next);
    if (!visibleKeys.includes(key)) return;
    setGrab({ itemId: item.id, targetKey: key });
    setAnnouncement(fmt.long.format(next));
  }

  const title =
    activeView === "month"
      ? fmt.month.format(anchor)
      : (() => {
          const first = weeks[0]?.[0] ?? anchor;
          const last = weeks[0]?.[6] ?? anchor;
          return `${fmt.short.format(first)} – ${fmt.shortYear.format(last)}`;
        })();

  const navButton =
    "inline-flex h-8 items-center justify-center rounded-md border border-border-strong bg-surface px-2.5 text-sm text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";
  const unit = activeView === "month" ? "month" : "week";

  return (
    <div className={className}>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <button type="button" className={navButton} onClick={() => setAnchor(today ?? now)}>
            Today
          </button>
          <button
            type="button"
            aria-label={`Previous ${unit}`}
            className={navButton}
            onClick={() => step(-1)}
          >
            <svg aria-hidden="true" viewBox="0 0 20 20" className="h-4 w-4" fill="currentColor">
              <path
                fillRule="evenodd"
                d="M12.79 5.23a.75.75 0 0 1-.02 1.06L8.83 10l3.94 3.71a.75.75 0 1 1-1.04 1.08l-4.5-4.25a.75.75 0 0 1 0-1.08l4.5-4.25a.75.75 0 0 1 1.06.02Z"
                clipRule="evenodd"
              />
            </svg>
          </button>
          <button
            type="button"
            aria-label={`Next ${unit}`}
            className={navButton}
            onClick={() => step(1)}
          >
            <svg aria-hidden="true" viewBox="0 0 20 20" className="h-4 w-4" fill="currentColor">
              <path
                fillRule="evenodd"
                d="M7.21 14.77a.75.75 0 0 1 .02-1.06L11.17 10 7.23 6.29a.75.75 0 1 1 1.04-1.08l4.5 4.25a.75.75 0 0 1 0 1.08l-4.5 4.25a.75.75 0 0 1-1.06-.02Z"
                clipRule="evenodd"
              />
            </svg>
          </button>
          <h2 className="ml-1 text-sm font-medium text-ink" aria-live="polite">
            {title}
          </h2>
        </div>
        <div role="group" aria-label="View" className="flex gap-1 rounded-lg bg-surface-muted p-1">
          {(["month", "week"] as const).map((v) => (
            <button
              key={v}
              type="button"
              aria-pressed={activeView === v}
              onClick={() => setView(v)}
              className={`rounded-md px-3 py-1 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent ${
                activeView === v
                  ? "bg-surface font-medium text-ink"
                  : "text-ink-muted hover:text-ink"
              }`}
            >
              {v === "month" ? "Month" : "Week"}
            </button>
          ))}
        </div>
      </div>

      {colors.size > 0 && (
        <ul
          aria-label="Channels"
          className="mb-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-muted"
        >
          {[...colors.values()].map((c) => (
            <li key={c.id} className="flex items-center gap-1.5">
              <span
                aria-hidden="true"
                className="h-2 w-2 rounded-full"
                style={{ backgroundColor: c.color }}
              />
              {c.label}
            </li>
          ))}
        </ul>
      )}

      {onMove && (
        <p id={hintId} className="sr-only">
          Press M to move this item, then arrow keys and Enter.
        </p>
      )}

      <table
        ref={gridRef}
        role="grid"
        aria-label={ariaLabel ?? title}
        className="w-full table-fixed border-collapse border border-border bg-surface"
      >
        <thead>
          <tr>
            {(weeks[0] ?? []).map((d) => (
              <th
                key={d.getDay()}
                scope="col"
                className="border-b border-border px-2 py-1.5 text-left text-xs font-normal text-ink-muted"
              >
                {fmt.weekday.format(d)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {weeks.map((week) => (
            <tr key={dayKey(week[0] ?? anchor)}>
              {week.map((day) => {
                const key = dayKey(day);
                const entries = byDay.get(key) ?? [];
                const outside = activeView === "month" && day.getMonth() !== anchor.getMonth();
                const isToday = key === todayKey;
                const isDrop = dropKey === key || grab?.targetKey === key;
                const limit = activeView === "month" ? maxItemsPerDay : Infinity;
                const shown = entries.slice(0, limit);
                const hidden = entries.length - shown.length;
                const isActive = key === activeKey;
                return (
                  <td
                    key={key}
                    role="gridcell"
                    data-day={key}
                    tabIndex={isActive ? 0 : -1}
                    aria-label={`${fmt.long.format(day)}${
                      entries.length
                        ? `, ${entries.length} ${entries.length === 1 ? "item" : "items"}`
                        : ""
                    }`}
                    aria-current={isToday ? "date" : undefined}
                    onKeyDown={(e) => handleCellKeyDown(e, day)}
                    onClick={(e) => handleCellClick(e, day)}
                    onDragOver={(e) => {
                      if (!dragId) return;
                      e.preventDefault();
                      e.dataTransfer.dropEffect = "move";
                      if (dropKey !== key) setDropKey(key);
                    }}
                    onDragLeave={(e) => {
                      if (
                        !e.currentTarget.contains(e.relatedTarget as Node | null) &&
                        dropKey === key
                      ) {
                        setDropKey(null);
                      }
                    }}
                    onDrop={(e) => {
                      e.preventDefault();
                      const id = e.dataTransfer.getData(itemMime) || dragId;
                      setDropKey(null);
                      setDragId(null);
                      if (id) commitMove(id, key);
                    }}
                    className={`border border-border p-1 align-top transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent ${
                      activeView === "week" ? "h-64" : "h-28"
                    } ${outside ? "bg-surface-sunken" : ""} ${
                      isDrop ? "bg-accent-surface ring-2 ring-inset ring-accent" : ""
                    } ${onDayClick ? "cursor-pointer hover:bg-ink/5" : ""}`}
                  >
                    <div className="mb-1 flex items-center justify-between px-1">
                      <span
                        aria-hidden="true"
                        className={`inline-flex h-6 min-w-6 items-center justify-center rounded-full text-xs tabular-nums ${
                          isToday
                            ? "bg-accent px-1.5 font-medium text-on-accent"
                            : outside
                              ? "text-ink-subtle"
                              : "text-ink"
                        }`}
                      >
                        {day.getDate()}
                      </span>
                    </div>
                    <ul className="space-y-1">
                      {shown.map(({ item, at }) => {
                        const channel = item.channel ? colors.get(item.channel) : undefined;
                        const color = channel?.color ?? "var(--color-ink-subtle)";
                        const time =
                          item.timeLabel ?? (isUntimed(item.date) ? "" : fmt.time.format(at));
                        const movable = Boolean(onMove) && item.movable !== false;
                        const grabbed = grab?.itemId === item.id;
                        return (
                          <li key={item.id}>
                            <button
                              type="button"
                              data-calendar-item={item.id}
                              tabIndex={isActive ? 0 : -1}
                              draggable={movable}
                              aria-describedby={movable ? hintId : undefined}
                              aria-keyshortcuts={movable ? "M" : undefined}
                              title={[time, item.title, channel?.label].filter(Boolean).join(" · ")}
                              onClick={() => onItemClick?.(item)}
                              onKeyDown={(e) => handleItemKeyDown(e, item, day)}
                              onBlur={() => {
                                if (grabbed) setGrab(null);
                              }}
                              onDragStart={(e) => {
                                e.dataTransfer.setData(itemMime, item.id);
                                e.dataTransfer.effectAllowed = "move";
                                setDragId(item.id);
                              }}
                              onDragEnd={() => {
                                setDragId(null);
                                setDropKey(null);
                              }}
                              style={{
                                borderLeftColor: color,
                                backgroundColor: `color-mix(in srgb, ${color} 14%, transparent)`,
                              }}
                              className={`flex w-full items-center gap-1 truncate rounded border-l-2 px-1.5 py-0.5 text-left text-xs text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent ${
                                movable ? "cursor-grab active:cursor-grabbing" : ""
                              } ${dragId === item.id || grabbed ? "opacity-60" : ""}`}
                            >
                              {time && (
                                <span className="shrink-0 tabular-nums text-ink-muted">{time}</span>
                              )}
                              <span className="truncate">{item.title}</span>
                              {channel && <span className="sr-only">, {channel.label}</span>}
                            </button>
                          </li>
                        );
                      })}
                    </ul>
                    {hidden > 0 && (
                      <button
                        type="button"
                        tabIndex={isActive ? 0 : -1}
                        onClick={() => {
                          setAnchor(day);
                          setView("week");
                        }}
                        className="mt-1 rounded px-1.5 text-xs text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                      >
                        +{hidden} more
                      </button>
                    )}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <span className="sr-only" aria-live="polite">
        {announcement}
      </span>
    </div>
  );
}
