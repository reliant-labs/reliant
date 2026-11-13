import React from "react";

/**
 * TimeSeriesChart — one or more series over time, as lines or filled areas,
 * with ANNOTATIONS: labelled vertical markers for the events that explain
 * the curve ("posted on HN", "pricing change", "v2 launch"). Hover, or focus
 * and use the arrow keys, to read every series at one moment.
 *
 *   <TimeSeriesChart
 *     ariaLabel="Signups and visits, last 30 days"
 *     series={[
 *       { id: "visits", label: "Visits", points: visits, variant: "area" },
 *       { id: "signups", label: "Signups", points: signups },
 *     ]}
 *     annotations={[{ t: "2026-11-04", label: "Posted on HN" }]}
 *     formatValue={(v) => v.toLocaleString()}
 *   />
 *
 * The library hand-rolls this one (no charting dependency, like the other
 * charts here) because the annotation is the point of it: a GTM dashboard
 * line is read against what was shipped or posted that day. For a plain
 * bar, pie or scatter, reach for Recharts.
 *
 * Time is any of: a Date, epoch milliseconds, an ISO timestamp, or a
 * date-only "YYYY-MM-DD" (local midnight). A `null` value is a gap, not a
 * zero. Width follows the container (ResizeObserver); height is a prop.
 *
 * Series colours default to the theme accent then a fixed categorical
 * palette; pass `color` (any CSS colour) to pin one.
 */
export type TimeValue = Date | number | string;

export interface TimeSeriesPoint {
  t: TimeValue;
  /** `null` breaks the line: no data, rather than zero. */
  value: number | null;
}

export interface TimeSeries {
  id: string;
  label: string;
  points: TimeSeriesPoint[];
  color?: string;
  /** Overrides the chart's `variant` for this series. */
  variant?: "line" | "area";
}

export interface TimeSeriesAnnotation {
  t: TimeValue;
  label: string;
  color?: string;
}

export interface TimeSeriesChartProps {
  series: TimeSeries[];
  annotations?: TimeSeriesAnnotation[];
  /** Accessible name; also the scrubber's label. */
  ariaLabel: string;
  /** Default series style. Defaults to "line". */
  variant?: "line" | "area";
  /** Plot height in px, axes included. Defaults to 240. */
  height?: number;
  /** Value formatter for the axis and tooltip. Defaults to compact numbers. */
  formatValue?: (value: number) => string;
  /** Time formatter for the tooltip. Defaults to a date (and time, for sub-day data). */
  formatTime?: (date: Date) => string;
  /** Fixed y range. Defaults to [min(0, data min), a rounded max]. */
  yDomain?: [number, number];
  /** Show the legend. Defaults to true when there are two or more series. */
  showLegend?: boolean;
  /** Shown instead of the plot when no series has a point. */
  emptyMessage?: React.ReactNode;
  className?: string;
}

const palette = [
  "var(--color-accent)",
  "#0ea5e9",
  "#f59e0b",
  "#10b981",
  "#ef4444",
  "#8b5cf6",
  "#ec4899",
];

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

function toTime(t: TimeValue): number {
  if (t instanceof Date) return t.getTime();
  if (typeof t === "number") return t;
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(t);
  if (m) return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])).getTime();
  return Date.parse(t);
}

/** Round steps of 1, 2 or 5 × 10^n, so axis labels read cleanly. */
function niceTicks(lo: number, hi: number, count: number): number[] {
  const span = hi - lo || Math.abs(hi) || 1;
  const raw = span / Math.max(1, count);
  const pow = 10 ** Math.floor(Math.log10(raw));
  const f = raw / pow;
  const stepSize = (f < 1.5 ? 1 : f < 3 ? 2 : f < 7 ? 5 : 10) * pow;
  const ticks: number[] = [];
  for (let v = Math.floor(lo / stepSize) * stepSize; v <= hi + stepSize * 0.5; v += stepSize) {
    ticks.push(Number(v.toPrecision(12)));
  }
  return ticks;
}

const timeSteps = [
  MINUTE,
  5 * MINUTE,
  15 * MINUTE,
  30 * MINUTE,
  HOUR,
  3 * HOUR,
  6 * HOUR,
  12 * HOUR,
  DAY,
  2 * DAY,
  7 * DAY,
  14 * DAY,
];

/** Tick times aligned to local minutes, days or months. */
function timeTicks(
  min: number,
  max: number,
  maxTicks: number,
): { ticks: number[]; unit: "time" | "day" | "month" } {
  const span = max - min;
  const want = span / Math.max(1, maxTicks);
  const stepMs = timeSteps.find((s) => s >= want);
  const ticks: number[] = [];
  if (stepMs !== undefined && stepMs < DAY) {
    for (let t = Math.ceil(min / stepMs) * stepMs; t <= max; t += stepMs) ticks.push(t);
    return { ticks, unit: "time" };
  }
  if (stepMs !== undefined) {
    const days = stepMs / DAY;
    const d = new Date(min);
    const cursor = new Date(d.getFullYear(), d.getMonth(), d.getDate());
    if (cursor.getTime() < min) cursor.setDate(cursor.getDate() + 1);
    while (cursor.getTime() <= max) {
      ticks.push(cursor.getTime());
      cursor.setDate(cursor.getDate() + days);
    }
    return { ticks, unit: "day" };
  }
  const months = [1, 2, 3, 6, 12, 24, 60].find((m) => m * 30 * DAY >= want) ?? 120;
  const d = new Date(min);
  const cursor = new Date(d.getFullYear(), d.getMonth() + 1, 1);
  while (cursor.getTime() <= max) {
    ticks.push(cursor.getTime());
    cursor.setMonth(cursor.getMonth() + months);
  }
  return { ticks, unit: "month" };
}

const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });

function defaultFormatValue(v: number): string {
  return compact.format(v);
}

/** Index of the time closest to t in a sorted array. */
function nearestIndex(times: number[], t: number): number {
  let lo = 0;
  let hi = times.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if ((times[mid] ?? 0) < t) lo = mid + 1;
    else hi = mid;
  }
  const prev = times[lo - 1];
  const here = times[lo] ?? t;
  return prev !== undefined && t - prev < here - t ? lo - 1 : lo;
}

const margin = { right: 12, bottom: 24, left: 48 };
const annotationRow = 16;

export default function TimeSeriesChart({
  series,
  annotations = [],
  ariaLabel,
  variant = "line",
  height = 240,
  formatValue = defaultFormatValue,
  formatTime,
  yDomain,
  showLegend,
  emptyMessage = "No data yet",
  className,
}: TimeSeriesChartProps) {
  const wrapRef = React.useRef<HTMLDivElement>(null);
  const [measured, setMeasured] = React.useState(0);
  const [active, setActive] = React.useState<number | null>(null);
  // useId's punctuation varies by React version; url(#…) wants a plain id.
  const gradientId = `tsc${React.useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;

  React.useEffect(() => {
    const el = wrapRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w) setMeasured(Math.round(w));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const width = measured || 640;

  const prepared = React.useMemo(
    () =>
      series.map((s, i) => {
        const points = s.points
          .map((p) => ({ t: toTime(p.t), value: p.value }))
          .filter((p) => Number.isFinite(p.t))
          .sort((a, b) => a.t - b.t);
        return {
          ...s,
          color: s.color ?? palette[i % palette.length] ?? "var(--color-accent)",
          variant: s.variant ?? variant,
          points,
          byTime: new Map(points.map((p) => [p.t, p.value])),
        };
      }),
    [series, variant],
  );

  const marks = React.useMemo(
    () =>
      annotations
        .map((a) => ({ ...a, time: toTime(a.t) }))
        .filter((a) => Number.isFinite(a.time))
        .sort((a, b) => a.time - b.time),
    [annotations],
  );

  const times = React.useMemo(
    () => [...new Set(prepared.flatMap((s) => s.points.map((p) => p.t)))].sort((a, b) => a - b),
    [prepared],
  );

  if (times.length === 0) {
    return (
      <div ref={wrapRef} className={className}>
        <div
          className="flex items-center justify-center rounded-lg border border-dashed border-border text-sm text-ink-muted"
          style={{ height }}
        >
          {emptyMessage}
        </div>
      </div>
    );
  }

  // ── Scales ──
  let tMin = Math.min(times[0] ?? 0, marks[0]?.time ?? Infinity);
  let tMax = Math.max(times[times.length - 1] ?? 0, marks[marks.length - 1]?.time ?? -Infinity);
  if (tMin === tMax) {
    tMin -= DAY / 2;
    tMax += DAY / 2;
  }
  const values = prepared
    .flatMap((s) => s.points.map((p) => p.value))
    .filter((v): v is number => v !== null);
  const dataMin = values.length ? Math.min(...values) : 0;
  const dataMax = values.length ? Math.max(...values) : 1;
  const niceY = niceTicks(yDomain?.[0] ?? Math.min(0, dataMin), yDomain?.[1] ?? dataMax, 4);
  const yLo = yDomain?.[0] ?? niceY[0] ?? 0;
  const yHi = yDomain?.[1] ?? niceY[niceY.length - 1] ?? 1;
  const yTicks = niceY.filter((v) => v >= yLo && v <= yHi);

  const plotLeft = margin.left;
  const plotRight = width - margin.right;

  // Annotation labels stack into rows so neighbours don't overprint.
  const rowEnds: number[] = [];
  const placed = marks.map((m) => {
    const x = plotLeft + ((m.time - tMin) / (tMax - tMin)) * (plotRight - plotLeft);
    const w = m.label.length * 6 + 8;
    const anchorEnd = x + w > plotRight;
    const left = anchorEnd ? x - w : x;
    let row = rowEnds.findIndex((end) => end < left);
    if (row === -1) {
      row = rowEnds.length < 3 ? rowEnds.length : rowEnds.length - 1;
    }
    rowEnds[row] = left + w;
    return { ...m, x, row, anchorEnd };
  });
  const plotTop = 8 + rowEnds.length * annotationRow;
  const plotBottom = height - margin.bottom;

  const x = (t: number) => plotLeft + ((t - tMin) / (tMax - tMin)) * (plotRight - plotLeft);
  const y = (v: number) => plotBottom - ((v - yLo) / (yHi - yLo || 1)) * (plotBottom - plotTop);
  const baseline = y(Math.max(yLo, Math.min(0, yHi)));

  const xTickInfo = timeTicks(tMin, tMax, Math.max(2, Math.floor((plotRight - plotLeft) / 90)));
  const xTickFormat = new Intl.DateTimeFormat(
    undefined,
    xTickInfo.unit === "time"
      ? { hour: "numeric", minute: "2-digit" }
      : xTickInfo.unit === "day"
        ? { month: "short", day: "numeric" }
        : { month: "short", year: "numeric" },
  );
  const subDay = times.some((t) => {
    const d = new Date(t);
    return d.getHours() !== 0 || d.getMinutes() !== 0;
  });
  const tooltipTime =
    formatTime ??
    ((d: Date) =>
      new Intl.DateTimeFormat(
        undefined,
        subDay
          ? { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }
          : { weekday: "short", month: "short", day: "numeric", year: "numeric" },
      ).format(d));

  // ── Paths ──
  const paths = prepared.map((s) => {
    const segments: Array<Array<{ t: number; value: number }>> = [];
    let current: Array<{ t: number; value: number }> = [];
    for (const p of s.points) {
      if (p.value === null) {
        if (current.length) segments.push(current);
        current = [];
      } else {
        current.push({ t: p.t, value: p.value });
      }
    }
    if (current.length) segments.push(current);
    const line = segments
      .map((seg) =>
        seg
          .map((p, i) => `${i === 0 ? "M" : "L"}${x(p.t).toFixed(1)},${y(p.value).toFixed(1)}`)
          .join(""),
      )
      .join("");
    const area = segments
      .map((seg) => {
        const first = seg[0];
        const last = seg[seg.length - 1];
        if (!first || !last) return "";
        const top = seg
          .map((p, i) => `${i === 0 ? "M" : "L"}${x(p.t).toFixed(1)},${y(p.value).toFixed(1)}`)
          .join("");
        return `${top}L${x(last.t).toFixed(1)},${baseline.toFixed(1)}L${x(first.t).toFixed(1)},${baseline.toFixed(1)}Z`;
      })
      .join("");
    return { ...s, line, area };
  });

  // ── Scrubber ──
  const activeTime = active !== null ? times[Math.min(active, times.length - 1)] : undefined;
  const activeIndexFor = (t: number) => nearestIndex(times, t);
  const activeMarks =
    activeTime === undefined ? [] : marks.filter((m) => activeIndexFor(m.time) === active);
  const readings =
    activeTime === undefined
      ? []
      : prepared
          .map((s) => ({
            id: s.id,
            label: s.label,
            color: s.color,
            value: s.byTime.get(activeTime),
          }))
          .filter(
            (r): r is { id: string; label: string; color: string; value: number } =>
              typeof r.value === "number",
          );
  const valueText =
    activeTime === undefined
      ? undefined
      : [
          tooltipTime(new Date(activeTime)),
          ...readings.map((r) => `${r.label} ${formatValue(r.value)}`),
          ...activeMarks.map((m) => m.label),
        ].join(", ");

  function move(index: number) {
    setActive(Math.max(0, Math.min(times.length - 1, index)));
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    const current = active ?? times.length - 1;
    const page = Math.max(1, Math.round(times.length / 10));
    const next =
      e.key === "ArrowLeft" || e.key === "ArrowDown"
        ? current - 1
        : e.key === "ArrowRight" || e.key === "ArrowUp"
          ? current + 1
          : e.key === "PageDown"
            ? current - page
            : e.key === "PageUp"
              ? current + page
              : e.key === "Home"
                ? 0
                : e.key === "End"
                  ? times.length - 1
                  : null;
    if (next === null) return;
    e.preventDefault();
    move(next);
  }

  function handlePointerMove(e: React.PointerEvent<HTMLDivElement>) {
    const rect = e.currentTarget.getBoundingClientRect();
    const px = ((e.clientX - rect.left) / (rect.width || width)) * width;
    const t = tMin + ((px - plotLeft) / (plotRight - plotLeft)) * (tMax - tMin);
    move(nearestIndex(times, t));
  }

  const legend = showLegend ?? prepared.length > 1;
  const activeX = activeTime === undefined ? null : x(activeTime);
  const tooltipLeft = activeX !== null && activeX > width / 2;

  return (
    <div ref={wrapRef} className={className}>
      {legend && (
        <ul
          aria-label="Series"
          className="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-muted"
        >
          {prepared.map((s) => (
            <li key={s.id} className="flex items-center gap-1.5">
              <span
                aria-hidden="true"
                className="h-0.5 w-3 rounded-full"
                style={{ backgroundColor: s.color }}
              />
              {s.label}
            </li>
          ))}
        </ul>
      )}
      <div
        role="slider"
        tabIndex={0}
        aria-label={ariaLabel}
        aria-valuemin={0}
        aria-valuemax={times.length - 1}
        aria-valuenow={active ?? times.length - 1}
        aria-valuetext={valueText ?? `${times.length} points; use the arrow keys to read them`}
        onKeyDown={handleKeyDown}
        onFocus={() => setActive((a) => a ?? times.length - 1)}
        onBlur={() => setActive(null)}
        onPointerMove={handlePointerMove}
        onPointerLeave={() => setActive(null)}
        className="relative w-full rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        style={{ height }}
      >
        <svg aria-hidden="true" width={width} height={height} className="block overflow-visible">
          <defs>
            {paths.map((s, i) => (
              <linearGradient key={s.id} id={`${gradientId}-${i}`} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={s.color} stopOpacity={0.24} />
                <stop offset="100%" stopColor={s.color} stopOpacity={0} />
              </linearGradient>
            ))}
          </defs>

          {yTicks.map((v) => (
            <g key={v}>
              <line
                x1={plotLeft}
                x2={plotRight}
                y1={y(v)}
                y2={y(v)}
                className="stroke-border"
                strokeWidth={1}
              />
              <text
                x={plotLeft - 8}
                y={y(v)}
                dy="0.32em"
                textAnchor="end"
                className="fill-ink-muted text-[11px] tabular-nums"
              >
                {formatValue(v)}
              </text>
            </g>
          ))}
          {xTickInfo.ticks.map((t) => (
            <text
              key={t}
              x={x(t)}
              y={plotBottom + 16}
              textAnchor="middle"
              className="fill-ink-muted text-[11px]"
            >
              {xTickFormat.format(new Date(t))}
            </text>
          ))}

          {placed.map((m, i) => (
            <g key={`${m.time}-${i}`}>
              <line
                x1={m.x}
                x2={m.x}
                y1={8 + m.row * annotationRow + 6}
                y2={plotBottom}
                stroke={m.color ?? "var(--color-ink-subtle)"}
                strokeDasharray="3 3"
                strokeWidth={1}
              />
              <text
                x={m.anchorEnd ? m.x - 4 : m.x + 4}
                y={8 + m.row * annotationRow}
                dy="0.71em"
                textAnchor={m.anchorEnd ? "end" : "start"}
                className="fill-ink-muted text-[11px]"
              >
                {m.label}
              </text>
            </g>
          ))}

          {paths.map((s, i) => (
            <g key={s.id}>
              {s.variant === "area" && <path d={s.area} fill={`url(#${gradientId}-${i})`} />}
              <path
                d={s.line}
                fill="none"
                stroke={s.color}
                strokeWidth={1.75}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            </g>
          ))}

          {activeX !== null && activeTime !== undefined && (
            <g>
              <line
                x1={activeX}
                x2={activeX}
                y1={plotTop}
                y2={plotBottom}
                className="stroke-border-strong"
                strokeWidth={1}
              />
              {readings.map((r) => (
                <circle
                  key={r.id}
                  cx={activeX}
                  cy={y(r.value)}
                  r={3.5}
                  fill={r.color}
                  className="stroke-surface"
                  strokeWidth={1.5}
                />
              ))}
            </g>
          )}
        </svg>

        {activeX !== null && activeTime !== undefined && (
          <div
            aria-hidden="true"
            className="pointer-events-none absolute z-10 min-w-36 rounded-md border border-border bg-surface px-2.5 py-2 text-xs shadow-md"
            style={{
              top: plotTop,
              left: tooltipLeft ? undefined : activeX + 12,
              right: tooltipLeft ? width - activeX + 12 : undefined,
            }}
          >
            <p className="mb-1 font-medium text-ink">{tooltipTime(new Date(activeTime))}</p>
            {readings.map((r) => (
              <p key={r.id} className="flex items-center justify-between gap-4 text-ink-muted">
                <span className="flex items-center gap-1.5">
                  <span className="h-2 w-2 rounded-full" style={{ backgroundColor: r.color }} />
                  {r.label}
                </span>
                <span className="font-medium text-ink tabular-nums">{formatValue(r.value)}</span>
              </p>
            ))}
            {activeMarks.map((m, i) => (
              <p key={i} className="mt-1 border-t border-border pt-1 text-ink">
                {m.label}
              </p>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
