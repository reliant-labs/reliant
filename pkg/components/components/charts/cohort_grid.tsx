import React from "react";

/**
 * CohortGrid — the retention triangle. One row per cohort (users who signed
 * up in a given week), one column per period since; each cell is the share
 * of the cohort still active, shaded by how high it is, with the count under
 * it. Recent cohorts have fewer periods, which is what makes the triangle.
 *
 *   <CohortGrid
 *     cohorts={[
 *       { label: "Oct 6", size: 412, retained: [412, 198, 151, 133] },
 *       { label: "Oct 13", size: 388, retained: [388, 201, 149] },
 *       { label: "Oct 20", size: 17, retained: [17, 9] },
 *     ]}
 *     minN={30}
 *   />
 *
 * `retained[p]` is the COUNT of the cohort active in period p; the
 * percentage is computed against `size`. A missing or null entry is a period
 * that has not happened yet and renders empty, unlike 0, which is a real
 * zero.
 *
 * Small samples lie. A cell whose n is under `minN` is dimmed and marked
 * "small sample" for screen readers. `dimBy` picks the n: "cohort" (the
 * default) compares the cohort's size — the denominator of every percentage
 * in the row — and "cell" compares the retained count itself.
 *
 * Shading is scaled to the highest retention after period 0 (period 0 is
 * usually 100% by definition and would flatten everything else to pale),
 * blended from the surface towards the theme accent.
 */
export interface Cohort {
  /** Row label: the cohort's start ("Oct 6", "2026-W41"). */
  label: string;
  /** Users in the cohort: the denominator. */
  size: number;
  /** Users active per period, from period 0. null/undefined = not yet. */
  retained: Array<number | null | undefined>;
}

export interface CohortGridProps {
  cohorts: Cohort[];
  /** Column header for a period. Defaults to "Week N". */
  periodLabel?: (period: number) => string;
  /** Dim cells whose n is below this. Defaults to 0 (nothing dimmed). */
  minN?: number;
  /** Which n `minN` compares: the cohort size (default) or the cell's count. */
  dimBy?: "cohort" | "cell";
  /** Add a size-weighted "All cohorts" row. Defaults to true. */
  showAverage?: boolean;
  /** Header of the cohort-size column. Defaults to "Users". */
  sizeLabel?: string;
  /** The table's caption, visually hidden. Defaults to "Retention by cohort". */
  caption?: React.ReactNode;
  /** Formats a 0–1 share. Defaults to whole percents, "<1%" for a sliver. */
  formatPercent?: (share: number) => string;
  className?: string;
}

function defaultFormatPercent(share: number): string {
  if (share > 0 && share < 0.005) return "<1%";
  return `${Math.round(share * 100)}%`;
}

interface Cell {
  n: number;
  share: number;
  base: number;
}

export default function CohortGrid({
  cohorts,
  periodLabel = (p) => `Week ${p}`,
  minN = 0,
  dimBy = "cohort",
  showAverage = true,
  sizeLabel = "Users",
  caption = "Retention by cohort",
  formatPercent = defaultFormatPercent,
  className,
}: CohortGridProps) {
  const periods = cohorts.reduce((max, c) => Math.max(max, c.retained.length), 0);
  const columns = Array.from({ length: periods }, (_, p) => p);

  const rows = cohorts.map((c) =>
    columns.map((p): Cell | null => {
      const n = c.retained[p];
      if (n === null || n === undefined) return null;
      return { n, share: c.size > 0 ? n / c.size : 0, base: c.size };
    }),
  );

  // Size-weighted average over the cohorts that have reached each period.
  const average = columns.map((p): Cell | null => {
    let n = 0;
    let base = 0;
    cohorts.forEach((c) => {
      const v = c.retained[p];
      if (v === null || v === undefined) return;
      n += v;
      base += c.size;
    });
    return base > 0 ? { n, share: n / base, base } : null;
  });

  let scaleMax = 0;
  for (const row of rows) {
    row.forEach((cell, p) => {
      if (cell && p > 0) scaleMax = Math.max(scaleMax, cell.share);
    });
  }
  if (scaleMax === 0) scaleMax = 1;

  function renderCell(cell: Cell | null, p: number, key: string, rowLabel: string) {
    if (!cell) {
      return (
        <td key={key} className="px-1 py-1">
          <span className="sr-only">Not yet</span>
        </td>
      );
    }
    const intensity = Math.min(1, cell.share / scaleMax);
    const small = minN > 0 && (dimBy === "cohort" ? cell.base : cell.n) < minN;
    const strong = intensity > 0.6;
    return (
      <td key={key} className="px-0.5 py-0.5">
        <div
          title={`${rowLabel}, ${periodLabel(p)}: ${cell.n.toLocaleString()} of ${cell.base.toLocaleString()} (${formatPercent(cell.share)})${small ? " — small sample" : ""}`}
          className={`flex h-11 min-w-14 flex-col items-center justify-center rounded tabular-nums ${
            strong ? "text-on-accent" : "text-ink"
          } ${small ? "opacity-40" : ""}`}
          style={{
            backgroundColor: `color-mix(in srgb, var(--color-accent) ${Math.round(6 + intensity * 74)}%, var(--color-surface))`,
          }}
        >
          <span className="text-xs font-medium">{formatPercent(cell.share)}</span>
          <span className={`text-[10px] ${strong ? "" : "text-ink-muted"}`}>
            <span className="sr-only">n = </span>
            {cell.n.toLocaleString()}
          </span>
          {small && <span className="sr-only">, small sample</span>}
        </div>
      </td>
    );
  }

  return (
    <div className={`overflow-x-auto ${className ?? ""}`}>
      <table className="w-full border-separate border-spacing-0 text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr>
            <th
              scope="col"
              className="sticky left-0 bg-surface px-2 py-1.5 text-left text-xs font-normal text-ink-muted"
            >
              Cohort
            </th>
            <th scope="col" className="px-2 py-1.5 text-right text-xs font-normal text-ink-muted">
              {sizeLabel}
            </th>
            {columns.map((p) => (
              <th
                key={p}
                scope="col"
                className="px-1 py-1.5 text-center text-xs font-normal text-ink-muted"
              >
                {periodLabel(p)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {cohorts.map((c, i) => (
            <tr key={`${c.label}-${i}`}>
              <th
                scope="row"
                className="sticky left-0 whitespace-nowrap bg-surface px-2 py-0.5 text-left text-xs font-medium text-ink"
              >
                {c.label}
              </th>
              <td className="px-2 py-0.5 text-right text-xs text-ink-muted tabular-nums">
                {c.size.toLocaleString()}
              </td>
              {(rows[i] ?? []).map((cell, p) => renderCell(cell, p, `${i}-${p}`, c.label))}
            </tr>
          ))}
        </tbody>
        {showAverage && cohorts.length > 1 && (
          <tfoot>
            <tr>
              <th
                scope="row"
                className="sticky left-0 whitespace-nowrap border-t border-border bg-surface px-2 pt-1.5 text-left text-xs font-medium text-ink"
              >
                All cohorts
              </th>
              <td className="border-t border-border px-2 pt-1.5 text-right text-xs text-ink-muted tabular-nums">
                {cohorts.reduce((sum, c) => sum + c.size, 0).toLocaleString()}
              </td>
              {average.map((cell, p) => renderCell(cell, p, `avg-${p}`, "All cohorts"))}
            </tr>
          </tfoot>
        )}
      </table>
    </div>
  );
}
