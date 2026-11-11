// Copyright (c) 2025 Reliant Labs

/**
 * A sql panel drawn as a heatmap (display.style: heatmap): its result as a
 * grid of cells shaded by value on one colour scale — a retention table's
 * cohorts down the side and weeks across, each cell darker for a larger share.
 *
 * What the rows and columns are follows the result's shape (forge dashspec
 * StyleHeatmap), so the statement decides it, not a setting:
 *
 *   wide    rows with text columns and numeric ones: a row of cells per row,
 *           named by its text columns, a column per numeric column.
 *   long    rows with exactly two text columns and one number: the first
 *           text column names the row, the second the column.
 *   series  a time series: a row per series, a column per bucket.
 *
 * A NULL is an empty cell, never a zero: a week that has not happened yet is
 * missing, not churned. The scale runs from the smallest value to the largest
 * the result holds, so a table of 2-4% retention still shows its contrast.
 *
 * It is a real table: the row and column names are headers, and each cell's
 * value is its text, so a screen reader reads it as one.
 */

import { useMemo } from "react";

import type { Panel, PanelResult, Unit } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { groupLabel } from "../obsFilters";
import { formatValue } from "../format";
import { CHART_PALETTE, namedToken, seriesLabel, tokenColor, unionTimestamps } from "./seriesModel";

export interface SqlMatrix {
  rows: string[];
  columns: string[];
  /** cells[row][column]; null is no value. */
  cells: (number | null)[][];
  min: number | null;
  max: number | null;
}

/** The grid a sql result draws as (see the shapes above). */
export function sqlMatrix(panel: Pick<Panel, "kind" | "series">, data: Pick<PanelResult, "rows" | "series" | "columns" | "bucketSeconds" | "bucketTimezone">, keys: string[]): SqlMatrix {
  let rows: string[];
  let columns: string[];
  let cells: (number | null)[][];
  if (data.series.length > 0) {
    const times = unionTimestamps(data.series);
    const daily = (data.bucketSeconds ?? 0) >= 86_400;
    columns = times.map((t) => bucketName(t, daily, data.bucketTimezone));
    rows = data.series.map((s) => seriesLabel(s, panel, keys));
    cells = data.series.map((s) => {
      const at = new Map(s.timestamps.map((t, i) => [t, s.values[i] ?? null]));
      return times.map((t) => finite(at.get(t)));
    });
  } else if (keys.length === 2 && data.columns.length === 1) {
    rows = [];
    columns = [];
    const index = (list: string[], name: string) => {
      const at = list.indexOf(name);
      if (at >= 0) return at;
      list.push(name);
      return list.length - 1;
    };
    const placed = data.rows.map((row) => ({ r: index(rows, row.group[keys[0]] ?? ""), c: index(columns, row.group[keys[1]] ?? ""), v: finite(row.values[0]) }));
    cells = rows.map(() => columns.map(() => null as number | null));
    for (const { r, c, v } of placed) cells[r][c] = v;
  } else {
    rows = data.rows.map((row) => groupLabel(row.group, keys) || "—");
    columns = [...data.columns];
    cells = data.rows.map((row) => columns.map((_, c) => finite(row.values[c])));
  }
  let min: number | null = null;
  let max: number | null = null;
  for (const v of cells.flat()) {
    if (v === null) continue;
    min = min === null ? v : Math.min(min, v);
    max = max === null ? v : Math.max(max, v);
  }
  return { rows, columns, cells, min, max };
}

function finite(v: number | null | undefined): number | null {
  return v === null || v === undefined || !Number.isFinite(v) ? null : v;
}

/** A bucket's column name: its date (in UTC when the points are calendar dates), or its time. */
function bucketName(ms: number, daily: boolean, zone: string | undefined): string {
  const at = new Date(ms);
  return daily
    ? at.toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: zone === "UTC" ? "UTC" : undefined })
    : at.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/** How strongly a value is shaded: 0.08 for the smallest to 0.72 for the largest, so the value printed on it stays legible in both schemes. */
export function cellStrength(v: number, min: number, max: number): number {
  if (!(max > min)) return 0.4;
  return 0.08 + 0.64 * ((v - min) / (max - min));
}

export function SqlHeatmap({ panel, data, unit, keys, height, compact }: { panel: Panel; data: PanelResult; unit: Unit; keys: string[]; height: number; compact: boolean }) {
  const grid = useMemo(() => sqlMatrix(panel, data, keys), [panel, data, keys]);
  const token = namedToken(panel.display, [], true) ?? CHART_PALETTE[0];
  const rows = compact ? grid.rows.slice(0, 6) : grid.rows;
  const shade = (v: number | null) => (v === null || grid.min === null || grid.max === null ? undefined : tokenColor(token, cellStrength(v, grid.min, grid.max)));
  if (compact) {
    return (
      <div
        className="grid gap-px"
        style={{ gridTemplateColumns: `repeat(${Math.max(grid.columns.length, 1)}, minmax(0, 1fr))` }}
        data-testid="obs-panel-sql-heatmap"
        aria-label={panel.title}
        role="img"
      >
        {rows.flatMap((_, r) => grid.cells[r].map((v, c) => <span key={`${r}:${c}`} className="h-1.5 rounded-[1px] bg-ink/[0.04]" style={{ backgroundColor: shade(v) }} />))}
      </div>
    );
  }
  return (
    <div className="overflow-auto" style={{ maxHeight: Math.max(height, 160) }} data-testid="obs-panel-sql-heatmap">
      <table className="w-full border-separate border-spacing-px text-2xs tabular-nums">
        <caption className="sr-only">{panel.title}</caption>
        <thead>
          <tr>
            <th scope="col" className="sticky left-0 top-0 z-10 bg-surface px-2 py-1 text-left font-medium text-ink-muted">
              {keys.length === 2 && data.series.length === 0 && data.columns.length === 1 ? keys[0] : keys.join(" · ")}
            </th>
            {grid.columns.map((name, c) => (
              <th key={`${name}:${c}`} scope="col" className="sticky top-0 bg-surface px-1 py-1 text-center font-medium text-ink-muted">
                {name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((name, r) => (
            <tr key={`${name}:${r}`}>
              <th scope="row" className="sticky left-0 whitespace-nowrap bg-surface px-2 py-1 text-left font-normal text-ink">
                {name}
              </th>
              {grid.cells[r].map((v, c) => (
                <td
                  key={c}
                  className={cn("min-w-[2.75rem] rounded-[2px] px-1 py-1 text-center", v === null ? "bg-ink/[0.03] text-ink-muted" : "text-ink")}
                  style={{ backgroundColor: shade(v) }}
                  title={`${name} · ${grid.columns[c]}: ${v === null ? "no value" : formatValue(v, unit)}`}
                  data-value={v === null ? undefined : v}
                >
                  {v === null ? <span aria-label="no value">·</span> : formatValue(v, unit)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-2 inline-flex items-center gap-1.5 text-2xs text-ink-muted" aria-hidden="true">
        {formatValue(grid.min, unit)}
        <span className="inline-block h-2 w-16 rounded-sm" style={{ background: `linear-gradient(to right, ${tokenColor(token, 0.08)}, ${tokenColor(token, 0.72)})` }} />
        {formatValue(grid.max, unit)}
      </p>
    </div>
  );
}
