// Copyright (c) 2025 Reliant Labs

/**
 * A heatmap panel: the distribution of one numeric field over time. Time
 * buckets run across, value buckets (the server's log-spaced bounds) run up,
 * and each cell's strength is how many events fell in it — on a log scale, so
 * a tail of a few slow requests is visible beside a peak of thousands.
 *
 * Cells are grid items placed by row and column, only the non-empty ones, so
 * the panel lays out like any other element and needs no coordinate maths.
 * Each cell names its bucket and count on hover; the axis labels name the
 * lowest and highest value bucket and the window's ends.
 */

import { useMemo } from "react";

import type { HeatmapCellResult, Unit } from "@/services/controlPlane/observability/types";

import { formatCount, formatValue } from "../format";
import { CHART_PALETTE, tokenColor } from "./seriesModel";

export interface HeatmapGrid {
  times: number[];
  /** Value buckets, lowest first. */
  buckets: { low: number; high: number }[];
  cells: { column: number; row: number; count: number; cell: HeatmapCellResult }[];
  max: number;
}

/**
 * The grid: one column per time bucket of the window when the step is known
 * (an empty bucket is an empty column, so time stays to scale), else one per
 * bucket that has events; one row per value bucket that has any.
 */
export function heatmapGrid(cells: readonly HeatmapCellResult[], stepSeconds = 0, window?: { startSec: number; endSec: number }): HeatmapGrid {
  const seen = new Set(cells.map((c) => c.timeSec));
  if (stepSeconds > 0 && window && (window.endSec - window.startSec) / stepSeconds <= MAX_COLUMNS) {
    for (let t = Math.floor(window.startSec / stepSeconds) * stepSeconds; t < window.endSec; t += stepSeconds) seen.add(t);
  }
  const times = [...seen].sort((a, b) => a - b);
  const bucketKey = (c: { low: number; high: number }) => `${c.low}:${c.high}`;
  const byKey = new Map<string, { low: number; high: number }>();
  cells.forEach((c) => byKey.set(bucketKey(c), { low: c.low, high: c.high }));
  const buckets = [...byKey.values()].sort((a, b) => a.low - b.low);
  const column = new Map(times.map((t, i) => [t, i]));
  const row = new Map(buckets.map((b, i) => [bucketKey(b), i]));
  let max = 0;
  const placed = cells.flatMap((c) => {
    if (!(c.count > 0)) return [];
    max = Math.max(max, c.count);
    return [{ column: column.get(c.timeSec)!, row: row.get(bucketKey(c))!, count: c.count, cell: c }];
  });
  return { times, buckets, cells: placed, max };
}

const MAX_COLUMNS = 720;

export function HeatmapChart({
  cells,
  unit,
  height,
  compact,
  label,
  stepSeconds,
  window,
}: {
  cells: readonly HeatmapCellResult[];
  unit: Unit;
  height: number;
  compact: boolean;
  label: string;
  stepSeconds?: number;
  window?: { startMs: number; endMs: number };
}) {
  const startSec = window ? window.startMs / 1000 : undefined;
  const endSec = window ? window.endMs / 1000 : undefined;
  const grid = useMemo(
    () => heatmapGrid(cells, stepSeconds ?? 0, startSec !== undefined && endSec !== undefined ? { startSec, endSec } : undefined),
    [cells, stepSeconds, startSec, endSec]
  );
  const { times, buckets, max } = grid;
  const strength = (count: number) => (max > 0 ? 0.12 + 0.88 * (Math.log1p(count) / Math.log1p(max)) : 0);
  const at = (sec: number) => new Date(sec * 1000).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  const range = (b: { low: number; high: number }) => `${formatValue(b.low, unit)}–${formatValue(b.high, unit)}`;
  const plot = (
    <div
      className="grid min-w-0 flex-1 gap-px rounded-sm bg-ink/[0.03]"
      style={{ height, gridTemplateColumns: `repeat(${Math.max(times.length, 1)}, minmax(0, 1fr))`, gridTemplateRows: `repeat(${Math.max(buckets.length, 1)}, minmax(0, 1fr))` }}
      aria-hidden="true"
    >
      {grid.cells.map(({ column, row, count, cell }) => (
        <span
          key={`${column}:${row}`}
          className="rounded-[1px]"
          // Highest value bucket at the top.
          style={{ gridColumn: column + 1, gridRow: buckets.length - row, backgroundColor: tokenColor(CHART_PALETTE[0], strength(count)) }}
          title={compact ? undefined : `${at(cell.timeSec)} · ${range(buckets[row])}: ${formatCount(count)} event${count === 1 ? "" : "s"}`}
        />
      ))}
    </div>
  );
  const busiest = grid.cells.reduce<HeatmapGrid["cells"][number] | null>((best, c) => (!best || c.count > best.count ? c : best), null);
  const caption = busiest
    ? `${label}: ${formatCount(grid.cells.reduce((sum, c) => sum + c.count, 0))} events from ${range(buckets[0])} to ${range(buckets[buckets.length - 1])}; the busiest cell is ${range(buckets[busiest.row])} at ${at(busiest.cell.timeSec)} with ${formatCount(busiest.count)}.`
    : `${label}: no events.`;
  if (compact) {
    return (
      <figure data-testid="obs-panel-heatmap" aria-label={label} role="group">
        {plot}
        <figcaption className="sr-only">{caption}</figcaption>
      </figure>
    );
  }
  return (
    <figure data-testid="obs-panel-heatmap" aria-label={label} role="group">
      <div className="flex gap-2">
        <div className="flex w-14 shrink-0 flex-col justify-between text-right text-2xs tabular-nums text-ink-muted" style={{ height }} aria-hidden="true">
          <span>{formatValue(buckets[buckets.length - 1]?.high ?? null, unit)}</span>
          <span>{formatValue(buckets[0]?.low ?? null, unit)}</span>
        </div>
        {plot}
      </div>
      <div className="ml-16 mt-1 flex justify-between text-2xs tabular-nums text-ink-muted" aria-hidden="true">
        <span>{times.length ? at(times[0]) : ""}</span>
        <span className="inline-flex items-center gap-1.5">
          fewer
          <span className="inline-block h-2 w-16 rounded-sm" style={{ background: `linear-gradient(to right, ${tokenColor(CHART_PALETTE[0], 0.12)}, ${tokenColor(CHART_PALETTE[0], 1)})` }} />
          more events
        </span>
        <span>{times.length ? at(times[times.length - 1]) : ""}</span>
      </div>
      <figcaption className="sr-only">{caption}</figcaption>
    </figure>
  );
}
