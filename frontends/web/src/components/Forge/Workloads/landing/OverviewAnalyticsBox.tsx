// Copyright (c) 2026 Reliant Labs

/**
 * The Overview's ANALYTICS box (U6e, owner): who used the app this week, at
 * a glance, and the way into Analytics.
 *
 *   headline    active users over the last 7 days (distinct user ids in
 *               product_events), against the 7 days before
 *   sparkline   active users per day
 *   top event   the custom event (`track()`, `product.<name>`) sent most often
 *
 * Three QueryPanel reads over product_events, the table forge's built-in
 * Product analytics dashboards and the Events & metrics page read, so the box
 * and those pages count the same events. With no product events in the
 * window, it says how to send some instead of drawing zeros.
 */

import { useMemo } from "react";
import { ArrowRight, BarChart3 } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { usePanelQuery } from "@/hooks/observability-queries";
import { LearnLink } from "@/lib/learnLinks";
import type { ObsScope, Panel, PanelResult } from "@/services/controlPlane/observability/types";

import { EVENTS_PANEL } from "../../Analytics/EventsMetricsPage";
import { Sparkline } from "../../LiveState/Sparkline";
import { formatCount } from "../../Observability/format";

const RANGE = "7d" as const;
const WEEK_MS = 7 * 24 * 3600 * 1000;

const SCOPE_WHERE = "Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} AND UserId != ''";

/** Active users and events over the window: one row. */
export const ANALYTICS_TOTALS_PANEL: Panel = {
  id: "overview-analytics-totals",
  title: "Active users",
  kind: "sql",
  signal: "",
  series: [],
  sql: `SELECT uniqExact(UserId) AS users, count() AS events FROM product_events WHERE ${SCOPE_WHERE}`,
};

/** Active users per day over the window. */
export const ANALYTICS_DAILY_PANEL: Panel = {
  id: "overview-analytics-daily",
  title: "Active users per day",
  kind: "sql",
  signal: "",
  series: [],
  minStep: "1d",
  sql: `SELECT toStartOfDay(Timestamp) AS t, uniqExact(UserId) AS users FROM product_events WHERE ${SCOPE_WHERE} GROUP BY t ORDER BY t`,
};

/** A one-row SQL answer's numbers by column: its rows carry them in `values`, by column name. */
export function rowNumber(result: PanelResult | undefined, column: string): number | null {
  const i = result?.columns.indexOf(column) ?? -1;
  return i >= 0 ? (result?.rows[0]?.values[i] ?? null) : null;
}

/** The per-day values: the first series when the server answers one, else the rows' `users` column in order. */
export function dailyValues(result: PanelResult | undefined): (number | null)[] {
  if (!result) return [];
  if (result.series[0]?.values.length) return result.series[0].values;
  const i = result.columns.indexOf("users");
  return i >= 0 ? result.rows.map((row) => row.values[i] ?? null) : [];
}

/** The custom event sent most often, `product.` dropped; null when the app sends none. */
export function topEvent(result: PanelResult | undefined): { name: string; events: number } | null {
  if (!result) return null;
  const at = result.columns.indexOf("events");
  for (const row of result.rows) {
    const event = row.group.event ?? "";
    if (event.startsWith("product.")) return { name: event.slice("product.".length), events: at >= 0 ? (row.values[at] ?? 0) : 0 };
  }
  return null;
}

export function OverviewAnalyticsBox({ scope, analyticsHref, eventsHref }: { scope: ObsScope | null; analyticsHref: string; eventsHref: string }) {
  const totals = usePanelQuery(scope, RANGE, ANALYTICS_TOTALS_PANEL, []);
  const previous = usePanelQuery(scope, RANGE, ANALYTICS_TOTALS_PANEL, [], [], { offsetKey: "period:7d", offsetMs: () => WEEK_MS });
  const daily = usePanelQuery(scope, RANGE, ANALYTICS_DAILY_PANEL, []);
  const events = usePanelQuery(scope, RANGE, EVENTS_PANEL, []);

  const users = rowNumber(totals.data, "users");
  const before = rowNumber(previous.data, "users");
  const sent = rowNumber(totals.data, "events");
  const values = useMemo(() => dailyValues(daily.data), [daily.data]);
  const top = useMemo(() => topEvent(events.data), [events.data]);
  const change = users !== null && before ? (users - before) / before : null;
  const loading = totals.isLoading;
  const none = !loading && (!users || users === 0) && !top;

  return (
    <section className="min-w-0 space-y-3" data-testid="overview-analytics">
      <div className="flex min-h-7 items-center justify-between gap-3">
        <h2 className="text-section text-ink">Analytics</h2>
        <Link href={analyticsHref} className="no-color -mr-2 inline-flex h-7 items-center gap-1 rounded-md px-2 text-meta text-ink-muted hover:bg-ink/5 hover:text-ink">
          Analytics
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </Link>
      </div>
      {loading ? (
        <div className="h-24 animate-pulse rounded-lg bg-ink/5 motion-reduce:animate-none" aria-label="Loading analytics" />
      ) : totals.error && !totals.data ? (
        <p className="text-ui text-ink-muted">Analytics could not be read right now.</p>
      ) : none ? (
        <div className="space-y-2 text-ui text-ink-muted" data-testid="overview-analytics-empty">
          <p className="flex items-center gap-2 text-ink">
            <BarChart3 className="size-4 text-ink-muted" aria-hidden="true" />
            No product events this week
          </p>
          <p>
            Send one with <code className="font-mono text-code">track(&quot;signup&quot;)</code> from the frontend, or{" "}
            <code className="font-mono text-code">observe.Track</code> from a service: active users, funnels and this box fill in from them.
          </p>
          <div className="flex items-center gap-3">
            <Link href={eventsHref} className="no-color text-primary-ink hover:underline">
              Events &amp; metrics
            </Link>
            <LearnLink section="analytics" />
          </div>
        </div>
      ) : (
        <div className="space-y-3">
          <div className="flex items-end justify-between gap-3">
            <div className="min-w-0">
              <div className="text-meta text-ink-muted">Active users, last 7 days</div>
              <div className="text-2xl font-semibold tabular-nums text-ink" data-testid="overview-analytics-users">
                {users === null ? "—" : formatCount(users)}
              </div>
              {change !== null && (
                <div className={change >= 0 ? "text-meta tabular-nums text-success-ink" : "text-meta tabular-nums text-warning-ink"}>
                  {change >= 0 ? "+" : ""}
                  {Math.round(change * 100)}% vs the 7 days before
                </div>
              )}
            </div>
            <Sparkline values={values} label="Active users per day, last 7 days" width={112} height={36} />
          </div>
          <dl className="grid grid-cols-2 gap-3 border-t border-border-subtle pt-3 text-meta">
            <div className="min-w-0">
              <dt className="text-ink-muted">Events sent</dt>
              <dd className="text-ui font-medium tabular-nums text-ink">{sent === null ? "—" : formatCount(sent)}</dd>
            </div>
            <div className="min-w-0">
              <dt className="text-ink-muted">Top event</dt>
              <dd className="truncate text-ui font-medium text-ink" title={top ? `product.${top.name}` : undefined} data-testid="overview-analytics-top-event">
                {top ? (
                  <>
                    <span className="font-mono text-code">{top.name}</span>
                    <span className="ml-1 font-normal tabular-nums text-ink-muted">{formatCount(top.events)}</span>
                  </>
                ) : (
                  <span className="font-normal text-ink-muted">none yet</span>
                )}
              </dd>
            </div>
          </dl>
        </div>
      )}
    </section>
  );
}
