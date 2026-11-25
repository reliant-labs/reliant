// Copyright (c) 2026 Reliant Labs

/**
 * The pure half of a chart's hover read-out: which sample is under the
 * pointer, where the arrow keys move it, what the tooltip says about it and
 * where the tooltip goes. useChartHover.ts is the stateful half, ChartTooltip
 * the bubble; UPlotChart, HeadlineBars and HeatmapChart all read through
 * them, so a dashboard line, a figure's sparkline, a gallery card's preview, a
 * headline's bars and a heatmap's column all read a sample the same way.
 */

import { formatValue, type ValueUnit } from "../format";

/** The sample nearest `sec` in ascending `timestamps`, or null when there are none. */
export function nearestIndex(timestamps: readonly number[], sec: number): number | null {
  const n = timestamps.length;
  if (n === 0 || !Number.isFinite(sec)) return null;
  let lo = 0;
  let hi = n - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (timestamps[mid]! < sec) lo = mid + 1;
    else hi = mid;
  }
  // lo is the first timestamp at or after sec: the one before it may be nearer.
  if (lo > 0 && sec - timestamps[lo - 1]! <= timestamps[lo]! - sec) return lo - 1;
  return lo;
}

/** The sample at `fraction` (0 at the left edge, 1 at the right) of a plot spanning `extent`. */
export function indexAtFraction(timestamps: readonly number[], extent: readonly [number, number], fraction: number): number | null {
  const clamped = Math.min(1, Math.max(0, fraction));
  return nearestIndex(timestamps, extent[0] + clamped * (extent[1] - extent[0]));
}

/**
 * The slot under viewport x `clientX` when a plot spanning `rect` is cut into
 * `length` equal slots, one per sample (a bar chart's buckets, a heatmap's
 * columns), clamped to the first and last; null when there are none.
 */
export function slotIndexAt(rect: { left: number; width: number }, clientX: number, length: number): number | null {
  if (length <= 0 || !(rect.width > 0) || !Number.isFinite(clientX)) return null;
  const fraction = (clientX - rect.left) / rect.width;
  return Math.min(length - 1, Math.max(0, Math.floor(fraction * length)));
}

/** Where slot `index` of `length` equal slots across `rect` sits: its centre, and the plot's top and bottom. */
export function slotAnchor(rect: { left: number; width: number; top: number; bottom: number }, index: number, length: number): SampleAnchor | null {
  if (length <= 0 || index < 0 || index >= length) return null;
  return { x: rect.left + ((index + 0.5) / length) * rect.width, top: rect.top, bottom: rect.bottom };
}

/**
 * Where a key moves the highlighted sample: ArrowLeft / ArrowRight to the
 * previous / next sample that has a value, Home / End to the first / last.
 * From no highlight, an arrow starts at the latest sample (the one a reader
 * usually wants first). Escape clears it (null). Any other key is not the
 * chart's (undefined), so the caller lets it through.
 */
export function stepSample(current: number | null, key: string, length: number, hasSample: (index: number) => boolean = () => true): number | null | undefined {
  if (length <= 0) return key === "Escape" ? null : undefined;
  const scan = (from: number, dir: 1 | -1): number | null => {
    for (let i = from; i >= 0 && i < length; i += dir) if (hasSample(i)) return i;
    return null;
  };
  const first = scan(0, 1);
  const last = scan(length - 1, -1);
  switch (key) {
    case "Escape":
      return null;
    case "Home":
      return first;
    case "End":
      return last;
    case "ArrowLeft":
      if (current === null) return last;
      return scan(current - 1, -1) ?? current;
    case "ArrowRight":
      if (current === null) return last;
      return scan(current + 1, 1) ?? current;
    default:
      return undefined;
  }
}

/**
 * The sample's time, as the tooltip heads it. A calendar bucket (a SQL Date,
 * read as its UTC midnight) is a date in that zone; anything else is an
 * instant in the reader's zone, with the date, since a window can span days.
 */
export function formatSampleTime(sec: number, calendar?: { timezone: "UTC" }): string {
  const date = new Date(sec * 1000);
  if (calendar) return date.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric", timeZone: calendar.timezone });
  return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

/** One line of the tooltip: a series, its value at the sample, and its colour (a CSS colour). */
export interface SampleRow {
  label: string;
  value: string;
  color: string;
  /** A comparison series (drawn dashed): its swatch is an outline. */
  dashed?: boolean;
}

/** The most series a tooltip lists; the rest are counted ("+3 more"). */
export const MAX_TOOLTIP_ROWS = 8;

/**
 * Each series' value at `index`, formatted in the chart's unit by the same
 * formatter the axis and the figures use. A series with no value there says
 * so ("—"). Values are each series' own, never a stacked running total.
 */
export function sampleRows(
  series: readonly { label: string; values: readonly (number | null)[]; dashed?: boolean }[],
  index: number,
  unit: ValueUnit | undefined,
  colorOf: (index: number) => string,
  format: (value: number | null, unit: ValueUnit | undefined) => string = formatValue
): SampleRow[] {
  return series.map((s, i) => {
    const raw = s.values[index];
    const value = raw === null || raw === undefined || !Number.isFinite(raw) ? null : raw;
    return { label: s.label, value: format(value, unit), color: colorOf(i), dashed: s.dashed || undefined };
  });
}

/** "Nov 18, 03:30 PM: Requests 12/s, Errors 0.3%." — what the live region says about a sample. */
export function describeSample(time: string, rows: readonly SampleRow[]): string {
  if (rows.length === 1) return `${time}: ${rows[0]!.value}.`;
  return `${time}: ${rows.map((r) => `${r.label} ${r.value}`).join(", ")}.`;
}

/** The sample's place on screen: its x, and the plot's top and bottom, in viewport pixels. */
export interface SampleAnchor {
  x: number;
  top: number;
  bottom: number;
}

const GAP = 12;
const MARGIN = 8;

/**
 * Where the tooltip goes: beside the crosshair (right of it, or left when it
 * would run off the viewport's right edge), vertically centred on the plot,
 * clamped inside the viewport. It never covers the highlighted sample.
 */
export function placeTooltip(
  anchor: SampleAnchor,
  size: { width: number; height: number },
  viewport: { width: number; height: number }
): { left: number; top: number; side: "right" | "left" } {
  const fitsRight = anchor.x + GAP + size.width <= viewport.width - MARGIN;
  const side = fitsRight || anchor.x - GAP - size.width < MARGIN ? "right" : "left";
  const rawLeft = side === "right" ? anchor.x + GAP : anchor.x - GAP - size.width;
  const left = Math.max(MARGIN, Math.min(rawLeft, viewport.width - MARGIN - size.width));
  const middle = (anchor.top + anchor.bottom) / 2;
  const top = Math.max(MARGIN, Math.min(middle - size.height / 2, viewport.height - MARGIN - size.height));
  return { left, top, side };
}
