// Copyright (c) 2025 Reliant Labs

/**
 * The legend of a multi-series panel, done the way Grafana's table legend and
 * Datadog's are: a colour swatch per series (the only way to tell which line
 * is which), its figures (latest, peak, total or average — sortable), and the
 * series itself as a control.
 *
 *   hover / focus a row   highlights its series; every other dims
 *   click a row           shows that series alone; click it again for all
 *   ⌘/Ctrl-click          adds it to, or takes it from, what is shown
 *   the → at a row's end  the drill-down into Logs and Traces (the explorer)
 *
 * With the pointer on the chart the first column reads the values in the
 * bucket under it, named by its time, so the legend doubles as the tooltip.
 * Past the panel's top N the rest fold into one "Other" row, the full list
 * behind "Show all"; a long list scrolls inside the panel rather than
 * growing it.
 */

import type React from "react";
import { ArrowRight } from "lucide-react";

import { cn } from "@/lib/utils";

import { formatValue } from "../format";
import type { Unit } from "@/services/controlPlane/observability/types";
import { tokenColor, type SeriesStats, type Stat } from "./seriesModel";

export interface LegendRow {
  key: string;
  label: string;
  token: string;
  repeat?: boolean;
  error?: boolean;
  stats: SeriesStats;
  /** The value in the bucket under the pointer. */
  atCursor?: number | null;
}

export interface LegendOther {
  /** "Other", "Other errors". */
  label: string;
  token: string;
  /** How many series were folded. */
  count: number;
  /** Their summed figures, when the values add up; absent when they do not (nothing is summed). */
  stats?: SeriesStats;
  atCursor?: number | null;
}

const STAT_LABEL: Record<Stat, string> = { last: "Last", max: "Max", total: "Total", avg: "Avg" };

export interface SeriesLegendProps {
  rows: LegendRow[];
  unit: Unit;
  columns: Stat[];
  sort: Stat;
  onSort: (stat: Stat) => void;
  /** The pointer's bucket, named ("14:05"); the first column then reads values there. */
  cursorLabel?: string | null;
  /** Keys shown alone; empty shows every row's series. */
  isolated: ReadonlySet<string>;
  highlighted: string | null;
  onHighlight: (key: string | null) => void;
  /** `additive`: ⌘/Ctrl-click, which adds or removes rather than replacing. */
  onToggle: (key: string, additive: boolean) => void;
  onReset: () => void;
  onDrill?: (key: string, event: React.MouseEvent | React.KeyboardEvent) => void;
  /** The folded tail: "Other", and "Other errors" for its error series. */
  others?: LegendOther[];
  /** Total series in the result, and whether all of them are listed. */
  total: number;
  /** How many series are drawn when not all are. */
  topN: number;
  showAll: boolean;
  /** Present when some series are folded or all are shown past the top N. */
  onShowAll?: (all: boolean) => void;
  compareLabel?: string;
  /** An accessible name for the table. */
  label: string;
}

export function SeriesLegend({
  rows,
  unit,
  columns,
  sort,
  onSort,
  cursorLabel,
  isolated,
  highlighted,
  onHighlight,
  onToggle,
  onReset,
  onDrill,
  others = [],
  total,
  topN,
  showAll,
  onShowAll,
  compareLabel,
  label,
}: SeriesLegendProps) {
  const isolating = isolated.size > 0;
  const atCursor = !!cursorLabel;
  return (
    <div className="mt-2 min-w-0" data-testid="obs-panel-legend" onMouseLeave={() => onHighlight(null)}>
      <div className="max-h-40 overflow-y-auto rounded border border-border/60" data-testid="obs-panel-legend-scroll">
        <table className="w-full table-fixed border-collapse text-xs" aria-label={label}>
          <thead className="sticky top-0 z-[1] bg-surface">
            <tr className="border-b border-border/60 text-ink-muted">
              <th scope="col" className="px-2 py-1 text-left font-medium">
                <span className="truncate">
                  {total} series{isolating ? ` · ${isolated.size} shown` : ""}
                </span>
              </th>
              {columns.map((stat, index) => {
                const live = index === 0 && atCursor;
                return (
                  <th key={stat} scope="col" className="w-[4.75rem] px-2 py-1 text-right font-medium" aria-sort={sort === stat && !live ? "descending" : undefined}>
                    {live ? (
                      <span className="tabular-nums text-ink" data-testid="obs-panel-legend-cursor">
                        {cursorLabel}
                      </span>
                    ) : (
                      <button
                        type="button"
                        onClick={() => onSort(stat)}
                        className={cn("rounded hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring", sort === stat && "text-ink")}
                        title={`Sort by ${STAT_LABEL[stat].toLowerCase()}`}
                      >
                        {STAT_LABEL[stat]}
                      </button>
                    )}
                  </th>
                );
              })}
              <th scope="col" className={cn("px-1 py-1", onDrill ? "w-7" : "w-0")}>
                <span className="sr-only">Open in explorer</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const shown = !isolating || isolated.has(row.key);
              const dim = (highlighted !== null && highlighted !== row.key) || !shown;
              return (
                <tr
                  key={row.key}
                  className={cn("group border-b border-border/40 last:border-b-0", highlighted === row.key && "bg-ink/5")}
                  data-series={row.label}
                  data-error={row.error || undefined}
                  data-shown={shown}
                  onMouseEnter={() => onHighlight(row.key)}
                >
                  <td className="min-w-0 px-2 py-0.5">
                    <button
                      type="button"
                      aria-pressed={isolating ? shown : false}
                      onClick={(event) => onToggle(row.key, event.metaKey || event.ctrlKey)}
                      onFocus={() => onHighlight(row.key)}
                      onBlur={() => onHighlight(null)}
                      className={cn(
                        "flex w-full min-w-0 items-center gap-1.5 rounded text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                        dim ? "text-ink-muted" : "text-ink"
                      )}
                      title={`${row.label}\nClick to show only this series; ⌘/Ctrl-click to add or remove it.`}
                    >
                      <Swatch token={row.token} dotted={row.repeat} faded={!shown} />
                      <span className="truncate">{row.label}</span>
                    </button>
                  </td>
                  {columns.map((stat, index) => (
                    <td key={stat} className={cn("px-2 py-0.5 text-right tabular-nums", dim ? "text-ink-muted" : "text-ink")}>
                      {formatValue(index === 0 && atCursor ? (row.atCursor ?? null) : row.stats[stat], unit)}
                    </td>
                  ))}
                  <td className="px-1 py-0.5 text-right">
                    {onDrill && (
                      <button
                        type="button"
                        onClick={(event) => onDrill(row.key, event)}
                        className="inline-flex h-5 w-5 items-center justify-center rounded text-ink-muted opacity-60 hover:bg-ink/5 hover:text-ink hover:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring group-hover:opacity-100"
                        aria-label={`Open ${row.label} in the explorer`}
                        title="Open in Logs or Traces"
                      >
                        <ArrowRight className="h-3 w-3" aria-hidden="true" />
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
            {others.map((other) => (
              <tr key={other.label} className="text-ink-muted" data-testid="obs-panel-legend-other">
                <td className="min-w-0 px-2 py-0.5">
                  <span className="flex min-w-0 items-center gap-1.5" title={other.stats ? "The folded series, summed" : "The folded series do not add up, so they are not drawn"}>
                    <Swatch token={other.token} faded={!other.stats || isolating} />
                    <span className="truncate">
                      {other.label} · {other.count} series{other.stats ? "" : " (not drawn)"}
                    </span>
                  </span>
                </td>
                {columns.map((stat, index) => (
                  <td key={stat} className="px-2 py-0.5 text-right tabular-nums">
                    {other.stats ? formatValue(index === 0 && atCursor ? (other.atCursor ?? null) : other.stats[stat], unit) : ""}
                  </td>
                ))}
                <td />
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {(onShowAll || isolating || compareLabel) && (
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-muted">
          {onShowAll && (
            <button type="button" onClick={() => onShowAll(!showAll)} className="rounded underline decoration-ink-muted/50 underline-offset-2 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" data-testid="obs-panel-legend-show-all">
              {showAll ? `Show the top ${topN}` : `Show all ${total}`}
            </button>
          )}
          {isolating && (
            <button type="button" onClick={onReset} className="rounded underline decoration-ink-muted/50 underline-offset-2 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" data-testid="obs-panel-legend-reset">
              Show every series
            </button>
          )}
          {compareLabel && <span>dashed: {compareLabel}</span>}
        </div>
      )}
    </div>
  );
}

/** A series' colour as a short stroke: solid, dotted for a palette repeat, faint for a hidden series. */
export function Swatch({ token, dotted = false, faded = false, className }: { token: string; dotted?: boolean; faded?: boolean; className?: string }) {
  return (
    <svg viewBox="0 0 14 8" className={cn("h-2 w-3.5 shrink-0", className)} aria-hidden="true">
      <line x1="1" y1="4" x2="13" y2="4" stroke={tokenColor(token, faded ? 0.35 : undefined)} strokeWidth="3" strokeLinecap="round" strokeDasharray={dotted ? "1 3" : undefined} />
    </svg>
  );
}
