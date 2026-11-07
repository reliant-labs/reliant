// Copyright (c) 2025 Reliant Labs

/**
 * A bar panel: one group per groupBy value, the top `limit` by the first
 * series, as the server returns them (rows, one value per series).
 *
 *   display.style bar (default)  bars, one per series within each group, side
 *                                by side or (display.stack) stacked; upright,
 *                                or along the y axis (display.horizontal),
 *                                which suits long group names
 *   display.style pie | donut    the groups as slices of the first series'
 *                                whole; the top N slices (display.topN, else
 *                                eight) and one "Other" for the rest
 *
 * Bars and slices are plain elements — percentages of a box, a conic
 * gradient — not hand-drawn SVG, so they lay out with the panel and need no
 * coordinate maths. A group that IS a failure ("result: error") is drawn in the
 * danger colour, and clicking a bar or a slice's row opens the drill-down.
 */

import { useMemo, useState } from "react";
import type React from "react";

import type { Panel, PanelResult, Unit } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";
import Link from "@/components/forge-ui/link";

import { formatPercent, formatValue } from "../format";
import { groupLabel } from "../obsFilters";
import { pointOf, type OnDrill } from "./SeriesChart";
import { CHART_PALETTE, DANGER_PALETTE, dealColors, foldTopN, intentOf, isErrorSeries, isErrorWord, OTHER_TOKEN, tokenColor, topNOf } from "./seriesModel";
import { Swatch } from "./SeriesLegend";

interface Props {
  panel: Panel;
  data: PanelResult;
  unit: Unit;
  keys: string[];
  onDrill?: OnDrill;
  dataRowHref?: (row: PanelResult["rows"][number]) => string | null;
  height: number;
  compact: boolean;
}

export function CategoryChart(props: Props) {
  const style = props.panel.display?.style;
  return <>
    {style === "pie" || style === "donut" ? <PieChart {...props} donut={style === "donut"} /> : <BarChart {...props} />}
    {props.dataRowHref && !props.compact && <ul className="mt-2 space-y-1 text-xs" aria-label="Data links">
      {props.data.rows.map((row, index) => {
        const href = props.dataRowHref!(row);
        const label = groupLabel(row.group, props.keys) || `Row ${index + 1}`;
        return <li key={index}>{href ? <Link href={href} className="text-primary-ink">{label}: Open in Data</Link>
          : <span aria-disabled="true" title="Target table, columns or row values are unavailable" className="text-ink-muted">{label}: Data unavailable</span>}</li>;
      })}
    </ul>}
  </>;
}

type Row = PanelResult["rows"][number];

const positive = (v: number | null | undefined): number => (v !== null && v !== undefined && Number.isFinite(v) && v > 0 ? v : 0);

function BarAction({ href, children, disabled, onClick, ...props }: React.ComponentProps<"button"> & { href?: string | null }) {
  if (href) return <Link href={href} className={props.className} aria-label={props["aria-label"]}>{children}</Link>;
  return <button {...props} type="button" disabled={disabled} onClick={onClick}>{children}</button>;
}

function BarChart({ panel, data, unit, keys, onDrill, dataRowHref, height, compact }: Props) {
  const rows = compact ? data.rows.slice(0, 5) : data.rows;
  const columns = data.columns;
  const stack = !!panel.display?.stack && columns.length > 1;
  const horizontal = !!panel.display?.horizontal;
  // One series: one colour, a failure group in danger. Several: a colour per series.
  const seriesColors = useMemo(
    () => dealColors(columns, (label) => isErrorSeries({ label, group: {} }, intentOf({ label }, panel))),
    [columns, panel]
  );
  const colorOf = (row: Row, column: number): string => {
    if (columns.length > 1) return seriesColors[column]?.token ?? CHART_PALETTE[0];
    return Object.values(row.group).some(isErrorWord) ? DANGER_PALETTE[0] : CHART_PALETTE[0];
  };
  const scale = Math.max(
    ...rows.map((row) => (stack ? row.values.reduce<number>((sum, v) => sum + positive(v), 0) : Math.max(0, ...row.values.map(positive)))),
    0
  );
  const pct = (v: number | null | undefined) => (scale > 0 ? (positive(v) / scale) * 100 : 0);
  const labelOf = (row: Row) => groupLabel(row.group, keys) || "—";
  const drill = (row: Row) => (event: React.MouseEvent) =>
    onDrill?.({ panel: panel.title, label: labelOf(row), group: row.group, signal: panel.signal }, pointOf(event));
  const title = (row: Row, column: number) => `${labelOf(row)}${columns.length > 1 ? ` — ${columns[column]}` : ""}: ${formatValue(row.values[column] ?? null, unit)}`;
  const segment = (row: Row, column: number, along: "width" | "height") => (
    <span
      key={column}
      className={cn("block shrink-0 transition-opacity", along === "height" ? "w-full" : "h-full", compact ? "" : "rounded-[2px]")}
      style={{ [along]: `${pct(row.values[column])}%`, backgroundColor: tokenColor(colorOf(row, column), 0.85) }}
      title={title(row, column)}
    />
  );
  const legend = !compact && columns.length > 1 && (
    <ul className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs text-ink-muted" data-testid="obs-panel-bar-legend">
      {columns.map((label, column) => (
        <li key={label} className="inline-flex items-center gap-1.5">
          <Swatch token={seriesColors[column]?.token ?? CHART_PALETTE[0]} />
          {label}
        </li>
      ))}
    </ul>
  );

  if (horizontal) {
    return (
      <div data-testid="obs-panel-bar" data-orientation="horizontal">
        <ul className={cn("space-y-1", !compact && "max-h-80 overflow-y-auto")}>
          {rows.map((row, index) => (
            <li key={index} className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] items-center gap-2 text-xs" data-group={labelOf(row)}>
              <span className="truncate text-ink" title={labelOf(row)}>
                {labelOf(row)}
              </span>
              <BarAction
                href={dataRowHref?.(row)}
                disabled={dataRowHref ? !dataRowHref(row) : !onDrill}
                onClick={dataRowHref ? undefined : drill(row)}
                className="flex h-3.5 min-w-0 items-stretch gap-px rounded-sm text-left hover:opacity-80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default disabled:hover:opacity-100"
                aria-label={`${labelOf(row)}: ${columns.map((c, i) => `${columns.length > 1 ? `${c} ` : ""}${formatValue(row.values[i] ?? null, unit)}`).join(", ")}`}
              >
                {stack ? (
                  columns.map((_, column) => segment(row, column, "width"))
                ) : (
                  <span className="flex min-w-0 flex-1 flex-col justify-center gap-px">
                    {columns.map((_, column) => (
                      <span key={column} className="block min-h-0 flex-1">
                        {segment(row, column, "width")}
                      </span>
                    ))}
                  </span>
                )}
              </BarAction>
              <span className="text-right tabular-nums text-ink-muted">{formatValue(row.values[0] ?? null, unit)}</span>
            </li>
          ))}
        </ul>
        {legend}
      </div>
    );
  }

  const showValues = !compact && columns.length === 1 && rows.length <= 12;
  return (
    <div data-testid="obs-panel-bar" data-orientation="vertical">
      <ul className="flex items-stretch gap-1.5" style={{ height }}>
        {rows.map((row, index) => (
          <li key={index} className="flex min-w-0 flex-1 flex-col items-stretch gap-1" data-group={labelOf(row)}>
            <BarAction
              href={dataRowHref?.(row)}
              disabled={dataRowHref ? !dataRowHref(row) : !onDrill}
              onClick={dataRowHref ? undefined : drill(row)}
              className={cn(
                "flex min-h-0 flex-1 rounded-sm hover:opacity-80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default disabled:hover:opacity-100",
                stack ? "flex-col-reverse items-stretch" : "items-end justify-center gap-px"
              )}
              aria-label={`${labelOf(row)}: ${columns.map((c, i) => `${columns.length > 1 ? `${c} ` : ""}${formatValue(row.values[i] ?? null, unit)}`).join(", ")}`}
            >
              {stack
                ? columns.map((_, column) => segment(row, column, "height"))
                : columns.map((_, column) => (
                    <span key={column} className="flex h-full min-w-0 max-w-12 flex-1 flex-col justify-end">
                      {showValues && <span className="mb-0.5 truncate text-center text-3xs tabular-nums text-ink-muted">{formatValue(row.values[column] ?? null, unit)}</span>}
                      {segment(row, column, "height")}
                    </span>
                  ))}
            </BarAction>
            {!compact && (
              <span className="truncate text-center text-2xs text-ink-muted" title={labelOf(row)}>
                {labelOf(row)}
              </span>
            )}
          </li>
        ))}
      </ul>
      {legend}
    </div>
  );
}

interface Slice {
  key: string;
  label: string;
  value: number;
  token: string;
  group: Record<string, string> | null;
}

/** The slices of a pie: positive first-series values, largest first, the tail folded into "Other". */
export function pieSlices(panel: Pick<Panel, "display">, data: Pick<PanelResult, "rows">, keys: string[]): Slice[] {
  const base = data.rows
    .map((row, index) => ({ key: String(index), label: groupLabel(row.group, keys) || "—", value: positive(row.values[0]), group: row.group }))
    .filter((s) => s.value > 0)
    .sort((a, b) => b.value - a.value);
  const colors = dealColors(base, (s) => isErrorSeries({ label: "", group: s.group }));
  const coloured = base.map((s, index) => ({ ...s, token: colors[index].token }));
  const { shown, rest } = foldTopN(coloured, topNOf(panel));
  if (rest.length === 0) return shown;
  return [...shown, { key: "other", label: `Other (${rest.length})`, value: rest.reduce((sum, s) => sum + s.value, 0), token: OTHER_TOKEN, group: null }];
}

function PieChart({ panel, data, unit, keys, onDrill, height, compact, donut }: Props & { donut: boolean }) {
  const slices = useMemo(() => pieSlices(panel, data, keys), [panel, data, keys]);
  const total = slices.reduce((sum, s) => sum + s.value, 0);
  const [hovered, setHovered] = useState<string | null>(null);
  const size = compact ? 28 : Math.min(Math.max(height, 96), 200);
  let at = 0;
  const stops = slices.map((s) => {
    const from = at;
    at += total > 0 ? (s.value / total) * 100 : 0;
    const color = tokenColor(s.token, hovered && hovered !== s.key ? 0.25 : undefined);
    return `${color} ${from}% ${at}%`;
  });
  const sliceAt = (event: React.MouseEvent<HTMLDivElement>): Slice | null => {
    const rect = event.currentTarget.getBoundingClientRect();
    const dx = event.clientX - (rect.left + rect.width / 2);
    const dy = event.clientY - (rect.top + rect.height / 2);
    const radius = rect.width / 2;
    const distance = Math.hypot(dx, dy);
    if (distance > radius || (donut && distance < radius * DONUT_HOLE)) return null;
    // 0 at twelve o'clock, clockwise, in percent: the conic gradient's own frame.
    const turn = ((Math.atan2(dx, -dy) / (2 * Math.PI)) * 100 + 100) % 100;
    let acc = 0;
    for (const s of slices) {
      acc += (s.value / total) * 100;
      if (turn <= acc) return s;
    }
    return null;
  };
  const drill = (slice: Slice, event: React.MouseEvent | React.KeyboardEvent) => {
    if (onDrill && slice.group) onDrill({ panel: panel.title, label: slice.label, group: slice.group, signal: panel.signal }, pointOf(event));
  };
  const pie = (
    <div
      className={cn("relative shrink-0 rounded-full", !compact && onDrill && "cursor-pointer")}
      style={{
        width: size,
        height: size,
        background: `conic-gradient(${stops.join(", ") || "transparent 0 100%"})`,
        ...(donut ? { mask: DONUT_MASK, WebkitMask: DONUT_MASK } : {}),
      }}
      onMouseMove={compact ? undefined : (event) => setHovered(sliceAt(event)?.key ?? null)}
      onMouseLeave={() => setHovered(null)}
      onClick={compact ? undefined : (event) => {
        const slice = sliceAt(event);
        if (slice) drill(slice, event);
      }}
      aria-hidden="true"
    />
  );
  if (compact) return <div data-testid="obs-panel-pie">{pie}</div>;
  return (
    <div className="flex flex-wrap items-center gap-4" data-testid="obs-panel-pie" data-style={donut ? "donut" : "pie"}>
      <div className="relative shrink-0">
        {pie}
        {donut && (
          <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center text-center">
            <span className="text-base font-semibold tabular-nums text-ink">{formatValue(total, unit)}</span>
            <span className="text-2xs text-ink-muted">total</span>
          </div>
        )}
      </div>
      <table className="min-w-[12rem] flex-1 border-collapse text-xs" aria-label={`${panel.title}: slices`} onMouseLeave={() => setHovered(null)}>
        <tbody className="block max-h-48 overflow-y-auto">
          {slices.map((s) => (
            <tr key={s.key} className={cn("table w-full table-fixed", hovered === s.key && "bg-ink/5")} onMouseEnter={() => setHovered(s.key)} data-slice={s.label}>
              <td className="px-1 py-0.5">
                {onDrill && s.group ? (
                  <button
                    type="button"
                    onClick={(event) => drill(s, event)}
                    onFocus={() => setHovered(s.key)}
                    onBlur={() => setHovered(null)}
                    className="flex w-full min-w-0 items-center gap-1.5 rounded text-left text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    title={`Open ${s.label} in the explorer`}
                  >
                    <Swatch token={s.token} />
                    <span className="truncate">{s.label}</span>
                  </button>
                ) : (
                  <span className="flex min-w-0 items-center gap-1.5 text-ink">
                    <Swatch token={s.token} />
                    <span className="truncate">{s.label}</span>
                  </span>
                )}
              </td>
              <td className="w-20 px-1 py-0.5 text-right tabular-nums text-ink">{formatValue(s.value, unit)}</td>
              <td className="w-14 px-1 py-0.5 text-right tabular-nums text-ink-muted">{formatPercent(total > 0 ? s.value / total : 0)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The donut's hole, as a fraction of its radius. */
const DONUT_HOLE = 0.6;
const DONUT_MASK = `radial-gradient(circle closest-side, transparent ${DONUT_HOLE * 100}%, black ${DONUT_HOLE * 100 + 1}%)`;
