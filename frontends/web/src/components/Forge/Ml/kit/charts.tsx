// Copyright (c) 2026 Reliant Labs

/**
 * The ML chart kit, over the console's own chart (Observability/charts
 * UPlotChart: its theme, tooltip, markers and bands):
 *
 *   MlChart     MlSeries → one chart; events become markers, segments bands;
 *               charts with one `syncKey` share a cursor; a step / tokens axis
 *               reads as steps; a series nothing reports is <NotCollected>.
 *   ChartTile   a small multiple: title, latest value, chart.
 *   Gantt       rows of state spans with marks (attempts, workloads, waits).
 *   Histogram   buckets with p50/p95/p99 and an optional comparison outline.
 *   Heatmap     rows × time cells (ranks, devices, pools).
 *   Sparkline   an inline SVG line for a table cell.
 */

import { useMemo, type ReactNode } from "react";

import { MlAxis, MlCollection, type MlEvent, type MlHistogram, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { UPlotChart, type ChartBand, type ChartMarker, type ChartSeries, type ChartTone } from "../../Observability/charts/UPlotChart";
import { fmtCompact, fmtStep, fmtUnit, num, tsMs } from "./format";
import { eventKindOf, TONE_VAR, type MlTone } from "./labels";
import { NotCollected } from "./ui";

const CHART_TONE: Record<MlTone, ChartTone | undefined> = { danger: "destructive", warning: "warning", success: "success", info: undefined, neutral: "muted" };

/** A step range (or a time range on a time axis) to shade: a data segment, a queue wait. */
export interface MlBand {
  from: number;
  to: number;
  label: string;
  tone?: MlTone;
}

export interface MlChartProps {
  series: readonly MlSeries[];
  /** Drawn as markers where they fall on the axis (a step axis uses each event's step). */
  events?: readonly MlEvent[];
  bands?: readonly MlBand[];
  label: string;
  height?: number;
  /** Charts sharing a key share one crosshair. */
  syncKey?: string;
  area?: "fill" | "stacked";
  bars?: boolean;
  compact?: boolean;
  legend?: boolean;
  /** Overrides the series' own unit. */
  unit?: string;
  /** What to call the metric in the not-collected placeholder. */
  what?: string;
  /** Per-series tone by index (e.g. the straggler's line in warning). */
  tones?: readonly (MlTone | undefined)[];
  testId?: string;
}

/** The union of the series' x values, sorted, and each series' y on it (null where it has none). */
export function alignSeries(series: readonly MlSeries[]): { xs: number[]; ys: (number | null)[][] } {
  const set = new Set<number>();
  for (const s of series) for (const x of s.x) set.add(x);
  const xs = [...set].sort((a, b) => a - b);
  const index = new Map(xs.map((x, i) => [x, i]));
  const ys = series.map((s) => {
    const out: (number | null)[] = new Array(xs.length).fill(null);
    s.x.forEach((x, i) => {
      const y = s.y[i];
      out[index.get(x)!] = Number.isFinite(y) ? y : null;
    });
    return out;
  });
  return { xs, ys };
}

function seriesLabel(s: MlSeries): string {
  const base = s.group || s.label || s.key;
  return s.stat ? `${base} ${s.stat}` : base;
}

export function MlChart({ series, events, bands, label, height = 160, syncKey, area, bars, compact, legend, unit, what, tones, testId }: MlChartProps) {
  const collected = useMemo(() => series.filter((s) => s.collection !== MlCollection.NOT_COLLECTED && s.collection !== MlCollection.NOT_APPLICABLE && s.x.length > 0), [series]);
  const axis = series[0]?.axis ?? MlAxis.TIME;
  const u = unit ?? series[0]?.unit ?? "";
  const { xs, ys } = useMemo(() => alignSeries(collected), [collected]);
  const chartSeries = useMemo<ChartSeries[]>(
    () =>
      collected.map((s, i) => {
        const tone = tones?.[series.indexOf(s)];
        return {
          label: seriesLabel(s),
          values: ys[i],
          ...(tone ? (CHART_TONE[tone] ? { tone: CHART_TONE[tone] } : { color: TONE_VAR[tone] }) : {}),
          ...(s.stat && s.stat !== "p50" && s.stat !== "mean" ? { dashed: true } : {}),
        };
      }),
    [collected, ys, tones, series],
  );
  const markers = useMemo<ChartMarker[]>(() => {
    if (!events?.length) return [];
    return events.flatMap((e) => {
      const at = axis === MlAxis.TIME ? (tsMs(e.at) ?? 0) / 1000 : num(e.step);
      if (!at) return [];
      const kind = eventKindOf(e.kind);
      return [{ timeSec: at, label: kind.label, description: e.title || kind.label, tone: CHART_TONE[kind.tone] ?? "muted" }];
    });
  }, [events, axis]);
  const chartBands = useMemo<ChartBand[]>(() => (bands ?? []).map((b) => ({ startSec: b.from, endSec: b.to, label: b.label, tone: b.tone ? CHART_TONE[b.tone] : "muted" })), [bands]);

  if (collected.length === 0) {
    const missing = series.find((s) => s.collection === MlCollection.NOT_COLLECTED);
    return <NotCollected what={what ?? label} source={missing?.missingSource} findingId={missing?.fixFindingId} height={compact ? undefined : height} compact={compact} />;
  }
  const xFormat = axis === MlAxis.STEP ? (x: number) => `step ${fmtStep(x)}` : axis === MlAxis.TOKENS ? (x: number) => `${fmtCompact(x)} tokens` : undefined;
  return (
    <div data-testid={testId} className="min-w-0">
      <UPlotChart
        timestamps={xs}
        series={chartSeries}
        format={(v) => fmtUnit(v, u)}
        height={height}
        label={label}
        compact={compact}
        legend={legend ?? (!compact && chartSeries.length > 1 && chartSeries.length <= 8)}
        markers={markers}
        bands={chartBands}
        area={area}
        bars={bars}
        syncKey={syncKey}
        xFormat={xFormat}
      />
    </div>
  );
}

/** The last collected value of a series, or null. */
export function lastValue(s: MlSeries | undefined): number | null {
  if (!s) return null;
  for (let i = s.y.length - 1; i >= 0; i--) if (Number.isFinite(s.y[i])) return s.y[i];
  return null;
}

/** A small multiple: its title and latest value on one line, the chart under it. */
export function ChartTile({ title, series, events, bands, syncKey, height = 110, unit, value, note, tones, testId, area }: Omit<MlChartProps, "label"> & { title: string; value?: ReactNode; note?: ReactNode }) {
  const u = unit ?? series[0]?.unit ?? "";
  const latest = value ?? (series.length === 1 && series[0].collection !== MlCollection.NOT_COLLECTED ? fmtUnit(lastValue(series[0]), u) : undefined);
  return (
    <div className="min-w-0 rounded-xl border border-border bg-surface px-3 pb-2 pt-2.5" data-testid={testId}>
      <div className="mb-1 flex items-baseline justify-between gap-2">
        <h3 className="truncate text-ui font-medium text-ink">{title}</h3>
        {latest !== undefined && <span className="shrink-0 text-ui tabular-nums text-ink">{latest}</span>}
      </div>
      {note && <p className="-mt-0.5 mb-1 truncate text-meta text-ink-muted">{note}</p>}
      <MlChart series={series} events={events} bands={bands} label={title} height={height} syncKey={syncKey} unit={unit} tones={tones} area={area} legend={false} what={title} />
    </div>
  );
}

// ── Gantt ─────────────────────────────────────────────────────────────────────

export interface GanttSpan {
  start: number;
  /** Null: still going (drawn to `now`). */
  end: number | null;
  state: string;
  label?: string;
  title?: string;
  href?: string;
}

export interface GanttMark {
  at: number;
  label: string;
  tone?: MlTone;
  shape?: "tick" | "diamond" | "cross" | "dot";
  title?: string;
  href?: string;
}

export interface GanttRow {
  id: string;
  label: ReactNode;
  sub?: ReactNode;
  spans: readonly GanttSpan[];
  marks?: readonly GanttMark[];
  selected?: boolean;
  onSelect?: () => void;
}

export interface GanttStateStyle {
  label: string;
  /** A CSS colour expression. */
  fill: string;
  /** Diagonal hatching (waiting states). */
  hatched?: boolean;
}

/** The default look of the states runs and workloads go through. */
export const GANTT_STATES: Record<string, GanttStateStyle> = {
  queued: { label: "Queued", fill: "hsl(var(--muted-foreground) / 0.35)", hatched: true },
  starting: { label: "Starting", fill: "hsl(var(--chart-1) / 0.4)" },
  recovering: { label: "Recovering", fill: "hsl(var(--destructive) / 0.45)", hatched: true },
  running: { label: "Running", fill: "hsl(var(--chart-1))" },
  checkpointing: { label: "Checkpointing", fill: "hsl(var(--chart-3))" },
  stalled: { label: "Stalled", fill: "hsl(var(--warning))" },
  restarting: { label: "Restarting", fill: "hsl(var(--destructive) / 0.6)" },
  succeeded: { label: "Done", fill: "hsl(var(--chart-1) / 0.75)" },
  pending: { label: "Pending", fill: "transparent" },
  training: { label: "Training", fill: "hsl(var(--chart-1))" },
  inference: { label: "Inference", fill: "hsl(var(--chart-2))" },
  eval: { label: "Eval", fill: "hsl(var(--chart-4))" },
  notebook: { label: "Notebook", fill: "hsl(var(--chart-5))" },
  other: { label: "Other", fill: "hsl(var(--chart-6))" },
};

const HATCH = "repeating-linear-gradient(135deg, transparent 0 3px, hsl(var(--surface) / 0.55) 3px 5px)";

/** Evenly spaced ticks across [from, to]. */
export function ticks(from: number, to: number, count = 6): number[] {
  if (!(to > from)) return [from];
  const step = (to - from) / (count - 1);
  return Array.from({ length: count }, (_, i) => from + i * step);
}

/**
 * Rows of spans on one axis (time in ms, or steps), with marks (checkpoints
 * as ticks, evals as diamonds, failures as crosses). Hover shows a span's
 * title; a span or mark with an href opens it; a row with onSelect scopes the
 * page to it.
 */
export function Gantt({
  rows,
  from,
  to,
  now,
  formatTick,
  states = GANTT_STATES,
  legendStates,
  labelWidth = 168,
  testId,
}: {
  rows: readonly GanttRow[];
  from: number;
  to: number;
  now?: number;
  formatTick: (v: number) => string;
  states?: Record<string, GanttStateStyle>;
  /** The states to show in the legend; all used ones when absent. */
  legendStates?: readonly string[];
  labelWidth?: number;
  testId?: string;
}) {
  const span = Math.max(1, to - from);
  const pct = (v: number) => `${((Math.min(Math.max(v, from), to) - from) / span) * 100}%`;
  const used = legendStates ?? [...new Set(rows.flatMap((r) => r.spans.map((s) => s.state)))].filter((s) => states[s] && s !== "pending");
  return (
    <div className="min-w-0 space-y-2" data-testid={testId}>
      <div className="relative">
        {rows.map((row) => (
          <div
            key={row.id}
            className={cn("group flex h-8 items-center border-b border-border-subtle last:border-0", row.onSelect && "cursor-pointer hover:bg-ink/5", row.selected && "bg-ink/5")}
            onClick={row.onSelect}
            data-testid={testId ? `${testId}-row-${row.id}` : undefined}
          >
            <div className="flex shrink-0 flex-col justify-center truncate pr-3 leading-tight" style={{ width: labelWidth }}>
              <span className="truncate text-ui text-ink">{row.label}</span>
              {row.sub && <span className="truncate text-meta text-ink-muted">{row.sub}</span>}
            </div>
            <div className="relative h-5 min-w-0 flex-1">
              {row.spans.map((s, i) => {
                const style = states[s.state] ?? GANTT_STATES.running;
                const end = s.end ?? now ?? to;
                const left = pct(s.start);
                const width = `max(2px, calc(${pct(end)} - ${left}))`;
                const body = (
                  <span
                    className={cn("absolute inset-y-0 flex items-center overflow-hidden rounded-sm px-1 text-meta leading-none text-ink", style.fill === "transparent" && "border border-dashed border-border")}
                    style={{ left, width, background: style.hatched ? `${HATCH}, ${style.fill}` : style.fill }}
                    title={s.title ?? `${style.label}${s.label ? `: ${s.label}` : ""}`}
                  >
                    {s.label && <span className="truncate text-2xs font-medium text-primary-foreground">{s.label}</span>}
                  </span>
                );
                return s.href ? (
                  <a key={i} href={s.href} className="no-color" onClick={(e) => e.stopPropagation()}>
                    {body}
                  </a>
                ) : (
                  <span key={i}>{body}</span>
                );
              })}
              {row.marks?.map((m, i) => (
                <GanttMarkGlyph key={`m${i}`} mark={m} left={pct(m.at)} />
              ))}
            </div>
          </div>
        ))}
        {now !== undefined && now > from && now < to && (
          <div className="pointer-events-none absolute inset-y-0 border-l border-dashed border-primary/70" style={{ left: `calc(${labelWidth}px + (100% - ${labelWidth}px) * ${(now - from) / span})` }} aria-hidden="true" />
        )}
      </div>
      <div className="flex items-center" aria-hidden="true">
        <div className="shrink-0" style={{ width: labelWidth }} />
        <div className="relative h-4 flex-1 text-meta text-ink-muted">
          {ticks(from, to).map((t, i, all) => (
            <span key={t} className={cn("absolute whitespace-nowrap tabular-nums", i === 0 ? "" : i === all.length - 1 ? "-translate-x-full" : "-translate-x-1/2")} style={{ left: pct(t) }}>
              {formatTick(t)}
            </span>
          ))}
        </div>
      </div>
      {used.length > 0 && (
        <ul className="flex flex-wrap gap-x-4 gap-y-1 pl-1 text-meta text-ink-muted">
          {used.map((s) => {
            const style = states[s];
            return (
              <li key={s} className="inline-flex items-center gap-1.5">
                <span className="h-2.5 w-4 rounded-sm" style={{ background: style.hatched ? `${HATCH}, ${style.fill}` : style.fill }} aria-hidden="true" />
                {style.label}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function GanttMarkGlyph({ mark, left }: { mark: GanttMark; left: string }) {
  const color = `hsl(var(${TONE_VAR[mark.tone ?? "neutral"]}))`;
  const shape = mark.shape ?? "tick";
  const glyph =
    shape === "diamond" ? (
      <span className="block h-2 w-2 rotate-45 border border-surface" style={{ backgroundColor: color }} />
    ) : shape === "cross" ? (
      <span className="block text-meta font-bold leading-none" style={{ color }}>
        ✕
      </span>
    ) : shape === "dot" ? (
      <span className="block h-2 w-2 rounded-full border border-surface" style={{ backgroundColor: color }} />
    ) : (
      <span className="block h-5 w-px" style={{ backgroundColor: color }} />
    );
  const body = (
    <span className="absolute top-1/2 z-10 -translate-x-1/2 -translate-y-1/2" style={{ left }} title={mark.title ?? mark.label}>
      {glyph}
    </span>
  );
  return mark.href ? (
    <a href={mark.href} className="no-color" onClick={(e) => e.stopPropagation()}>
      {body}
    </a>
  ) : (
    body
  );
}

// ── Histogram ─────────────────────────────────────────────────────────────────

/** A distribution: bars per bucket, the percentiles as rules, a comparison as outlines. */
export function Histogram({ histogram, height = 120, testId }: { histogram: MlHistogram; height?: number; testId?: string }) {
  const h = histogram;
  if (h.collection === MlCollection.NOT_COLLECTED) return <NotCollected what={h.label} source={h.missingSource} height={height} />;
  const buckets = h.buckets;
  const max = Math.max(1, ...buckets.map((b) => num(b.count)), ...h.compareBuckets.map((b) => num(b.count)));
  const lo = buckets[0]?.lower ?? 0;
  const hi = buckets[buckets.length - 1]?.upper ?? 1;
  const at = (v: number) => `${((Math.min(Math.max(v, lo), hi) - lo) / (hi - lo || 1)) * 100}%`;
  const f = (v: number) => fmtUnit(v, h.unit);
  return (
    <div className="space-y-1" data-testid={testId}>
      <div className="relative flex items-end gap-px" style={{ height }} role="img" aria-label={`${h.label}: p50 ${f(h.p50)}, p95 ${f(h.p95)}, p99 ${f(h.p99)}`}>
        {buckets.map((b, i) => {
          const cmp = h.compareBuckets[i];
          return (
            <div key={i} className="relative h-full flex-1" title={`${f(b.lower)}–${f(b.upper)}: ${num(b.count).toLocaleString()}`}>
              <div className="absolute inset-x-0 bottom-0 rounded-t-sm" style={{ height: `${(num(b.count) / max) * 100}%`, backgroundColor: "hsl(var(--chart-1) / 0.8)" }} />
              {cmp && <div className="absolute inset-x-0 bottom-0 rounded-t-sm border border-b-0 border-dashed border-ink-muted" style={{ height: `${(num(cmp.count) / max) * 100}%` }} />}
            </div>
          );
        })}
        {(
          [
            ["p50", h.p50],
            ["p95", h.p95],
            ["p99", h.p99],
          ] as const
        ).map(([name, v]) =>
          Number.isFinite(v) && v > 0 ? (
            <div key={name} className="pointer-events-none absolute inset-y-0 border-l border-ink/60" style={{ left: at(v) }}>
              <span className="absolute -top-0.5 left-1 whitespace-nowrap text-2xs tabular-nums text-ink-muted">
                {name} {f(v)}
              </span>
            </div>
          ) : null,
        )}
      </div>
      <div className="flex justify-between text-meta tabular-nums text-ink-muted">
        <span>{f(lo)}</span>
        {h.compareLabel && (
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2 w-3 rounded-sm border border-dashed border-ink-muted" aria-hidden="true" /> {h.compareLabel}
          </span>
        )}
        <span>{f(hi)}</span>
      </div>
    </div>
  );
}

// ── Heatmap ───────────────────────────────────────────────────────────────────

export interface HeatmapRow {
  id: string;
  label: ReactNode;
  /** 0–1 by default (`max` rescales); null is "no sample". */
  cells: readonly (number | null)[];
  tone?: MlTone;
  href?: string;
}

/**
 * Rows × columns of cells, one hue by value (02-views §0.5: distributions
 * over fleets). A row with a tone is called out (the straggler).
 */
export function Heatmap({
  rows,
  columns,
  max = 1,
  format,
  onCell,
  labelWidth = 120,
  cellHeight = 14,
  testId,
}: {
  rows: readonly HeatmapRow[];
  /** A label per column (times); only a few are drawn. */
  columns: readonly string[];
  max?: number;
  format: (v: number) => string;
  onCell?: (row: HeatmapRow, col: number) => void;
  labelWidth?: number;
  cellHeight?: number;
  testId?: string;
}) {
  const step = Math.max(1, Math.ceil(columns.length / 6));
  return (
    <div className="min-w-0 space-y-1" data-testid={testId}>
      {rows.map((row) => (
        <div key={row.id} className="flex items-center" style={{ height: cellHeight + 2 }}>
          <div className={cn("shrink-0 truncate pr-2 text-right text-meta tabular-nums", row.tone && row.tone !== "neutral" ? "font-medium text-warning-ink" : "text-ink-muted")} style={{ width: labelWidth }}>
            {row.href ? (
              <a href={row.href} className="no-color hover:underline">
                {row.label}
              </a>
            ) : (
              row.label
            )}
          </div>
          <div className="flex min-w-0 flex-1 gap-px">
            {row.cells.map((v, i) => (
              <div
                key={i}
                className={cn("flex-1 rounded-sm", onCell && "cursor-pointer hover:outline hover:outline-1 hover:outline-ink")}
                style={{
                  height: cellHeight,
                  background: v === null ? "repeating-linear-gradient(135deg, transparent 0 2px, hsl(var(--border)) 2px 3px)" : `hsl(var(--chart-1) / ${0.08 + 0.92 * Math.min(1, Math.max(0, v / max))})`,
                }}
                title={`${typeof row.label === "string" ? row.label : row.id} · ${columns[i] ?? ""}: ${v === null ? "no sample" : format(v)}`}
                onClick={onCell ? () => onCell(row, i) : undefined}
              />
            ))}
          </div>
        </div>
      ))}
      <div className="flex" aria-hidden="true">
        <div className="shrink-0" style={{ width: labelWidth }} />
        <div className="relative h-4 flex-1 text-meta text-ink-muted">
          {columns.map((c, i) =>
            i % step === 0 ? (
              <span key={i} className="absolute -translate-x-1/2 whitespace-nowrap tabular-nums" style={{ left: `${((i + 0.5) / columns.length) * 100}%` }}>
                {c}
              </span>
            ) : null,
          )}
        </div>
      </div>
    </div>
  );
}

// ── Sparkline ─────────────────────────────────────────────────────────────────

/** An inline line for a table cell: values left to right, NaN as a gap. */
export function Sparkline({ values, width = 72, height = 20, tone, testId }: { values: readonly number[]; width?: number; height?: number; tone?: MlTone; testId?: string }) {
  const finite = values.filter((v) => Number.isFinite(v));
  if (finite.length < 2) return <span className="text-meta text-ink-muted">—</span>;
  const min = Math.min(...finite);
  const max = Math.max(...finite);
  const range = max - min || 1;
  let d = "";
  let pen = false;
  values.forEach((v, i) => {
    if (!Number.isFinite(v)) {
      pen = false;
      return;
    }
    const x = (i / (values.length - 1)) * (width - 2) + 1;
    const y = height - 1 - ((v - min) / range) * (height - 2);
    d += `${pen ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`;
    pen = true;
  });
  return (
    <svg width={width} height={height} className="inline-block align-middle" aria-hidden="true" data-testid={testId}>
      <path d={d} fill="none" stroke={tone ? `hsl(var(${TONE_VAR[tone]}))` : "hsl(var(--chart-1))"} strokeWidth={1.5} strokeLinejoin="round" />
    </svg>
  );
}
