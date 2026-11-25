// Copyright (c) 2026 Reliant Labs

/**
 * The Overview's ANALYTICS box's reads (OverviewAnalyticsBox): the panels it
 * queries over product_events and how it reads their answers. Apart from the
 * component, so the reading is tested without rendering a chart.
 */

import type { Panel, PanelResult } from "@/services/controlPlane/observability/types";

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

/**
 * The per-day counts and the day each starts (epoch seconds): the first series
 * when the server answers one, else the rows' `users` column against their
 * `t`. Mismatched series arrays are rejected; points without a finite day are
 * left out. Timestamp/value pairs are sorted together, without mutating the answer.
 */
export function dailySeries(result: PanelResult | undefined): { timestamps: number[]; values: (number | null)[] } {
  const none = { timestamps: [], values: [] };
  if (!result) return none;
  const first = result.series[0];
  if (first && first.timestamps.length !== first.values.length) return none;
  if (first?.values.length) {
    const points = first.timestamps
      .flatMap((sec, index) => (Number.isFinite(sec) ? [{ sec, users: first.values[index] ?? null }] : []))
      .sort((a, b) => a.sec - b.sec);
    return { timestamps: points.map((p) => p.sec), values: points.map((p) => p.users) };
  }
  const i = result.columns.indexOf("users");
  if (i < 0) return none;
  const days = result.rows.flatMap((row) => {
    const sec = sqlTimeSeconds(row.group.t);
    return sec === null ? [] : [{ sec, users: row.values[i] ?? null }];
  }).sort((a, b) => a.sec - b.sec);
  return { timestamps: days.map((d) => d.sec), values: days.map((d) => d.users) };
}

/** ClickHouse's Date or DateTime text ("2026-11-20", "2026-11-20 00:00:00", in UTC) or an ISO instant, as epoch seconds. */
function sqlTimeSeconds(text: string | undefined): number | null {
  if (!text) return null;
  const iso = /^\d{4}-\d\d-\d\d$/.test(text) ? `${text}T00:00:00` : text.includes("T") ? text : text.replace(" ", "T");
  const ms = Date.parse(/(Z|[+-]\d\d:?\d\d)$/.test(iso) ? iso : `${iso}Z`);
  return Number.isFinite(ms) ? ms / 1000 : null;
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
