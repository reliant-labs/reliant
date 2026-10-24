// Copyright (c) 2025 Reliant Labs

/**
 * The chart's legend as a table: each series with its colour, its last, average
 * and largest value over the complete buckets (an open bucket is not a reading
 * yet), and the controls to hide it or show it alone. Hidden series are view
 * state — they ride in the URL (`hide=`) and are never sent to the server.
 */

import { Eye, EyeOff } from "lucide-react";

import { cn } from "@/lib/utils";

import { StackedTable } from "@/components/Forge/StackedTable";
import { formatValue, type ValueUnit } from "../format";
import { TONE_SWATCH, type DisplaySeries } from "./ExplorerChart";
import { seriesFigures } from "./seriesView";

export interface SeriesTableProps {
  series: readonly DisplaySeries[];
  unit: ValueUnit;
  hidden: readonly string[];
  onHidden: (hidden: string[]) => void;
}

export function SeriesTable({ series, unit, hidden, onHidden }: SeriesTableProps) {
  if (series.length === 0) return null;
  const labels = series.map((s) => s.label);
  const soloed = (label: string) => hidden.length === labels.length - 1 && !hidden.includes(label);
  return (
    <div className="max-h-72 overflow-auto">
      <StackedTable className="w-full text-xs" data-testid="metrics-series-table">
        <caption className="sr-only">Series: last, average and largest value</caption>
        <thead>
          <tr className="border-b border-border text-left text-ink-muted">
            <th scope="col" className="h-8 pr-3 font-medium">
              Series
            </th>
            <th scope="col" className="h-8 pr-3 text-right font-medium">
              Last
            </th>
            <th scope="col" className="h-8 pr-3 text-right font-medium">
              Avg
            </th>
            <th scope="col" className="h-8 pr-3 text-right font-medium">
              Max
            </th>
            <th scope="col" className="h-8 font-medium">
              <span className="sr-only">Show</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {series.map((s) => {
            const off = hidden.includes(s.label);
            const figures = seriesFigures(s.values, s.partial);
            return (
              <tr key={s.label} className={cn("border-b border-border/70 last:border-b-0", off && "text-ink-muted")} data-series={s.label}>
                <th scope="row" className="h-8 max-w-[24rem] pr-3 text-left font-normal">
                  <button
                    type="button"
                    onClick={() => onHidden(off ? hidden.filter((l) => l !== s.label) : [...hidden, s.label])}
                    aria-pressed={!off}
                    title={off ? "Show this series" : "Hide this series"}
                    className="inline-flex max-w-full items-center gap-2 truncate rounded text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                    data-testid="metrics-series-toggle"
                  >
                    <span className={cn("h-2.5 w-2.5 shrink-0 rounded-sm", TONE_SWATCH[s.tone], off && "opacity-30")} aria-hidden="true" />
                    <span className={cn("truncate", off ? "text-ink-muted line-through" : "text-ink")}>{s.label}</span>
                    {off ? <EyeOff className="h-3 w-3 shrink-0" aria-hidden="true" /> : <Eye className="h-3 w-3 shrink-0 opacity-0" aria-hidden="true" />}
                  </button>
                </th>
                <td className="h-8 pr-3 text-right tabular-nums">{formatValue(figures.last, unit)}</td>
                <td className="h-8 pr-3 text-right tabular-nums">{formatValue(figures.avg, unit)}</td>
                <td className="h-8 pr-3 text-right tabular-nums">{formatValue(figures.max, unit)}</td>
                <td className="h-8 text-right">
                  {series.length > 1 && (
                    <button
                      type="button"
                      onClick={() => onHidden(soloed(s.label) ? [] : labels.filter((l) => l !== s.label))}
                      aria-pressed={soloed(s.label)}
                      className="rounded px-1.5 py-0.5 text-2xs text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                      aria-label={soloed(s.label) ? `Show every series again` : `Show only ${s.label}`}
                      data-testid="metrics-series-solo"
                    >
                      {soloed(s.label) ? "all" : "solo"}
                    </button>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </StackedTable>
    </div>
  );
}
