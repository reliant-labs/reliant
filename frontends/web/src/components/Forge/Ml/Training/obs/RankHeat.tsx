// Copyright (c) 2026 Reliant Labs

/**
 * Ranks × time, dense: one 7px row per rank (64 ranks fit beside a panel),
 * a label at each node's first rank and at the called-out rank, the colour
 * scaled to the rows' own range so a 0.69-against-0.77 SM-active gap is
 * visible. A cell opens that rank.
 */

import { cn } from "@/lib/utils";

import type { HeatRow } from "./obsModel";

export function heatRange(rows: readonly HeatRow[]): { lo: number; hi: number } {
  let lo = Infinity;
  let hi = -Infinity;
  for (const r of rows)
    for (const v of r.cells) {
      if (v === null) continue;
      if (v < lo) lo = v;
      if (v > hi) hi = v;
    }
  if (!Number.isFinite(lo)) return { lo: 0, hi: 1 };
  return hi - lo < 1e-9 ? { lo: lo - 0.5, hi: hi + 0.5 } : { lo, hi };
}

export function RankHeat({
  rows,
  columns,
  format,
  callout,
  selected,
  nodeOf,
  onRank,
  testId,
}: {
  rows: readonly HeatRow[];
  columns: readonly string[];
  format: (v: number) => string;
  callout: number | null;
  selected: number | null;
  /** The node a rank runs on (a label at each node's first rank). */
  nodeOf: (rank: number) => string;
  onRank: (rank: number) => void;
  testId?: string;
}) {
  const { lo, hi } = heatRange(rows);
  const tick = Math.max(1, Math.ceil(columns.length / 6));
  return (
    <div className="min-w-0 space-y-1" data-testid={testId}>
      <div className="flex flex-col gap-px">
        {rows.map((row, i) => {
          const firstOfNode = i === 0 || nodeOf(row.rank) !== nodeOf(rows[i - 1].rank);
          const hot = row.rank === callout;
          return (
            <div key={row.rank} className={cn("flex h-2 items-center", firstOfNode && i > 0 && "mt-0.5")} data-testid={testId ? `${testId}-r${row.rank}` : undefined}>
              <div className={cn("w-24 shrink-0 truncate pr-2 text-right text-meta leading-none tabular-nums", hot ? "font-medium text-warning-ink" : "text-ink-muted")}>
                {hot ? `▸ r${row.rank}` : firstOfNode ? `r${row.rank} · ${nodeOf(row.rank)}` : row.rank === selected ? `r${row.rank}` : ""}
              </div>
              <div className={cn("flex h-full min-w-0 flex-1 gap-px", row.rank === selected && "outline outline-1 outline-ink")}>
                {row.cells.map((v, c) => (
                  <button
                    type="button"
                    key={c}
                    className="h-full flex-1 rounded-sm hover:outline hover:outline-1 hover:outline-ink"
                    style={{ background: v === null ? "hsl(var(--border))" : hot ? `hsl(var(--warning) / ${0.15 + 0.85 * ((v - lo) / (hi - lo))})` : `hsl(var(--chart-1) / ${0.1 + 0.9 * ((v - lo) / (hi - lo))})` }}
                    title={`rank ${row.rank} · ${columns[c] ?? ""}: ${v === null ? "no sample" : format(v)}`}
                    aria-label={`rank ${row.rank} at ${columns[c] ?? ""}`}
                    onClick={() => onRank(row.rank)}
                  />
                ))}
              </div>
            </div>
          );
        })}
      </div>
      <div className="flex" aria-hidden="true">
        <div className="w-24 shrink-0" />
        <div className="relative h-4 flex-1 text-meta text-ink-muted">
          {columns.map((c, i) =>
            i % tick === 0 ? (
              <span key={i} className="absolute -translate-x-1/2 whitespace-nowrap tabular-nums" style={{ left: `${((i + 0.5) / columns.length) * 100}%` }}>
                {c}
              </span>
            ) : null,
          )}
        </div>
      </div>
      <div className="flex items-center justify-end gap-2 text-meta tabular-nums text-ink-muted">
        {format(lo)}
        <span className="h-2 w-24 rounded-sm" style={{ background: "linear-gradient(to right, hsl(var(--chart-1) / 0.1), hsl(var(--chart-1)))" }} aria-hidden="true" />
        {format(hi)}
      </div>
    </div>
  );
}
