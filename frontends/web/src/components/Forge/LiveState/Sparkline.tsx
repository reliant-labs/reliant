// Copyright (c) 2026 Reliant Labs

/**
 * A word-sized line: the shape of a series, no axes. For a row in a list,
 * where a chart would be too much and a number alone hides the trend.
 *
 * It reads the sample under the pointer like every observability chart
 * (UPlotChart's "HOVER", through the same useChartHover and ChartTooltip): a
 * rule and a dot on the sample, and a tooltip with its value — and its time,
 * when the caller hands over `timestamps`. A finger taps or drags along it;
 * the keyboard focuses it and steps with the arrow keys, Home and End.
 */

import { useCallback, useId, useMemo, useRef } from "react";

import { cn } from "@/lib/utils";

import { ChartTooltip } from "../Observability/charts/ChartTooltip";
import { describeSample, formatSampleTime, type SampleAnchor, type SampleRow } from "../Observability/charts/chartHover";
import { tokenColor } from "../Observability/charts/chartPalette";
import { useChartHover } from "../Observability/charts/useChartHover";

export function Sparkline({
  values,
  label,
  className,
  width = 64,
  height = 18,
  timestamps,
  format = defaultFormat,
}: {
  values: readonly (number | null)[];
  /** What it shows, for a screen reader: "CPU over the last 24 hours". */
  label: string;
  className?: string;
  width?: number;
  height?: number;
  /** Epoch seconds, one per value: the tooltip heads each sample with its time. Absent: its position ("3 of 12"). */
  timestamps?: readonly number[];
  /** A value as the row beside it shows it (formatRate, formatPercent…). */
  format?: (value: number) => string;
}) {
  const ref = useRef<SVGSVGElement>(null);
  const hintId = useId();
  const points = values.flatMap((v, i) => (v === null || !Number.isFinite(v) ? [] : [{ i, v }]));
  const max = points.length ? Math.max(...points.map((p) => p.v)) : 0;
  const min = points.length ? Math.min(...points.map((p) => p.v)) : 0;
  const span = max - min || 1;
  const last = values.length - 1 || 1;
  const xOf = (i: number) => (i / last) * width;
  const yOf = (v: number) => height - 2 - ((v - min) / span) * (height - 4);

  const hasSample = useCallback((i: number) => isPresent(values[i]), [values]);
  const anchorOf = useCallback(
    (i: number): SampleAnchor | null => {
      const rect = ref.current?.getBoundingClientRect();
      return rect ? { x: rect.left + (i / last) * rect.width, top: rect.top, bottom: rect.bottom } : null;
    },
    [last]
  );
  const indexAt = useCallback(
    (clientX: number): number | null => {
      const rect = ref.current?.getBoundingClientRect();
      if (!rect || rect.width <= 0 || values.length === 0) return null;
      return Math.round(Math.min(1, Math.max(0, (clientX - rect.left) / rect.width)) * last);
    },
    [last, values.length]
  );
  const { hover, bind, plotStyle } = useChartHover({ rootRef: ref, length: values.length, hasSample, anchorOf, indexAt });

  const at = hover ? values[hover.index] : null;
  const time = hover ? (timestamps?.[hover.index] !== undefined ? formatSampleTime(timestamps[hover.index]!) : `${hover.index + 1} of ${values.length}`) : "";
  const rows = useMemo<SampleRow[]>(() => (hover ? [{ label, value: isPresent(at) ? format(at) : "—", color: tokenColor("--primary") }] : []), [hover, at, label, format]);

  if (points.length < 2) {
    return <span className={cn("inline-block text-meta text-ink-muted", className)} style={{ width }} aria-label={`${label}: no samples`}>—</span>;
  }
  const d = points.map((p, n) => `${n === 0 ? "M" : "L"}${xOf(p.i).toFixed(1)},${yOf(p.v).toFixed(1)}`).join(" ");
  return (
    <>
      <svg
        ref={ref}
        role="img"
        aria-label={label}
        aria-describedby={hintId}
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        className={cn("inline-block overflow-visible stroke-primary", className)}
        style={plotStyle}
        {...bind}
        data-testid="sparkline"
      >
        <path d={d} fill="none" strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
        {hover && (
          <g aria-hidden="true" data-testid="sparkline-cursor">
            <line x1={xOf(hover.index)} x2={xOf(hover.index)} y1={0} y2={height} className="stroke-ink-muted" strokeWidth={1} strokeDasharray="2 2" vectorEffect="non-scaling-stroke" />
            {isPresent(at) && <circle cx={xOf(hover.index)} cy={yOf(at)} r={2.5} className="fill-primary stroke-surface" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />}
          </g>
        )}
      </svg>
      <span id={hintId} className="sr-only">
        Left and right arrow keys read each sample.
      </span>
      <span className="sr-only" aria-live="polite" aria-atomic="true">
        {hover?.via === "keyboard" ? describeSample(time, rows) : ""}
      </span>
      {hover && <ChartTooltip anchor={hover.anchor} time={time} rows={rows} />}
    </>
  );
}

function isPresent(value: number | null | undefined): value is number {
  return value !== null && value !== undefined && Number.isFinite(value);
}

function defaultFormat(value: number): string {
  return value.toLocaleString(undefined, { maximumFractionDigits: 2 });
}
