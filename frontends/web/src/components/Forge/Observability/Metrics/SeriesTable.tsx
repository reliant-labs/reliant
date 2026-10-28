// Copyright (c) 2025 Reliant Labs

/**
 * The chart's legend as a table: each series with its colour, its last, average
 * and largest value over the complete buckets (an open bucket is not a reading
 * yet), and the controls to hide it or show it alone. Hidden series are view
 * state — they ride in the URL (`hide=`) and are never sent to the server.
 */

import { Eye, EyeOff } from "lucide-react";

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { cn } from "@/lib/utils";

import { formatValue, type ValueUnit } from "../format";
import { TONE_SWATCH, type DisplaySeries } from "./ExplorerChart";
import { seriesFigures } from "./seriesView";

export interface SeriesTableProps {
  series: readonly DisplaySeries[];
  unit: ValueUnit;
  hidden: readonly string[];
  onHidden: (hidden: string[]) => void;
}

type SeriesRow = { series: DisplaySeries };

export function SeriesTable({ series, unit, hidden, onHidden }: SeriesTableProps) {
  if (series.length === 0) return null;
  const labels = series.map((s) => s.label);
  const soloed = (label: string) => hidden.length === labels.length - 1 && !hidden.includes(label);
  const figuresOf = new Map(series.map((s) => [s.label, seriesFigures(s.values, s.partial)]));
  const columns: DataTableColumn<SeriesRow>[] = [
    {
      key: "series",
      header: "Series",
      interactive: true,
      className: "max-w-[24rem]",
      render: (_, { series: s }) => {
        const off = hidden.includes(s.label);
        return (
          <button
            type="button"
            onClick={() => onHidden(off ? hidden.filter((l) => l !== s.label) : [...hidden, s.label])}
            aria-pressed={!off}
            title={off ? "Show this series" : "Hide this series"}
            className="inline-flex max-w-full items-center gap-2 truncate rounded text-left text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            data-testid="metrics-series-toggle"
          >
            <span className={cn("h-2.5 w-2.5 shrink-0 rounded-sm", TONE_SWATCH[s.tone], off && "opacity-30")} aria-hidden="true" />
            <span className={cn("truncate", off ? "text-ink-muted line-through" : "text-ink")}>{s.label}</span>
            {off ? <EyeOff className="h-3 w-3 shrink-0" aria-hidden="true" /> : <Eye className="h-3 w-3 shrink-0 opacity-0" aria-hidden="true" />}
          </button>
        );
      },
    },
    { key: "last", header: "Last", numeric: true, render: (_, { series: s }) => <span className="text-xs">{formatValue(figuresOf.get(s.label)!.last, unit)}</span> },
    { key: "avg", header: "Avg", numeric: true, render: (_, { series: s }) => <span className="text-xs">{formatValue(figuresOf.get(s.label)!.avg, unit)}</span> },
    { key: "max", header: "Max", numeric: true, render: (_, { series: s }) => <span className="text-xs">{formatValue(figuresOf.get(s.label)!.max, unit)}</span> },
    {
      key: "show",
      header: "Show",
      srHeader: true,
      interactive: true,
      width: "3rem",
      className: "px-2 text-right",
      render: (_, { series: s }) =>
        series.length > 1 && (
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
        ),
    },
  ];
  return (
    <div className="max-h-72 overflow-auto" data-testid="metrics-series-table">
      <DataTable<SeriesRow>
        flush
        stacked
        compact
        ariaLabel="Series: last, average and largest value"
        columns={columns}
        data={series.map((s) => ({ series: s }))}
        getRowKey={({ series: s }) => s.label}
        getRowProps={({ series: s }) => ({ className: cn(hidden.includes(s.label) && "text-ink-muted"), "data-series": s.label })}
      />
    </div>
  );
}
