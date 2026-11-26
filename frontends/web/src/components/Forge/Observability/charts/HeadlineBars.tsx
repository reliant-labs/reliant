// Copyright (c) 2026 Reliant Labs

/**
 * The chart under a page's headline number when the page counts things per
 * bucket (runs started, error events by hour): bars on one baseline, no
 * gridlines, the window's first bucket and "Now" under it.
 *
 * A plain SVG rather than the console's uPlot canvas (UPlotChart): these
 * pages draw one small histogram, and loading a canvas chart library for it
 * would cost every page that leads with one, in the bundle and in jsdom.
 * Colour is `--chart-1`, the palette's first.
 *
 * It reads the bucket under the pointer like every chart (UPlotChart's
 * "HOVER", through the same useChartHover and ChartTooltip): the bucket's
 * column is shaded, its bar drawn solid, and a tooltip states its time and
 * count. A finger taps or drags along it; the keyboard focuses it and steps
 * with the arrow keys, Home and End.
 */

import { useCallback, useId, useMemo, useRef } from "react";

import { formatCount } from "../format";
import { ChartTooltip } from "./ChartTooltip";
import { describeSample, formatSampleTime, slotAnchor, slotIndexAt, type SampleAnchor, type SampleRow } from "./chartHover";
import { tokenColor } from "./chartPalette";
import { useChartHover } from "./useChartHover";

const SLOT = 10;
const PLOT = 100;

export function HeadlineBars({
  values,
  timestamps,
  label,
  testId,
  height = 112,
  format = formatCount,
}: {
  values: readonly number[];
  /** Epoch seconds, one per bucket. */
  timestamps: readonly number[];
  /** The figure's accessible name ("Runs started, last 24 hours"). */
  label: string;
  testId?: string;
  /** CSS pixels. */
  height?: number;
  /** A bucket's count as the tooltip states it ("12 runs"). Default: the count. */
  format?: (value: number) => string;
}) {
  const figureRef = useRef<HTMLElement>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  const hintId = useId();
  const length = values.length;

  const hasSample = useCallback(() => true, []);
  const anchorOf = useCallback(
    (index: number): SampleAnchor | null => {
      const rect = svgRef.current?.getBoundingClientRect();
      return rect ? slotAnchor(rect, index, length) : null;
    },
    [length]
  );
  const indexAt = useCallback((clientX: number) => {
    const rect = svgRef.current?.getBoundingClientRect();
    return rect ? slotIndexAt(rect, clientX, length) : null;
  }, [length]);
  const { hover, bind, plotStyle } = useChartHover({ rootRef: figureRef, length, hasSample, anchorOf, indexAt });

  const sampleSec = hover ? timestamps[hover.index] : undefined;
  const time = sampleSec === undefined ? "" : formatSampleTime(sampleSec);
  const value = hover ? values[hover.index] : undefined;
  const rows = useMemo<SampleRow[]>(
    () => (value === undefined ? [] : [{ label, value: format(value), color: tokenColor("--chart-1") }]),
    [value, label, format]
  );

  if (length === 0) return null;
  const max = Math.max(1, ...values);
  const width = length * SLOT;
  const span = (timestamps[timestamps.length - 1] ?? 0) - (timestamps[0] ?? 0);
  const fmt = (sec: number) =>
    new Date(sec * 1000).toLocaleString(undefined, span <= 86_400 ? { hour: "numeric" } : { month: "short", day: "numeric" });
  return (
    <figure ref={figureRef} className="space-y-1.5" data-testid={testId} role="group" aria-label={label} aria-describedby={hintId} {...bind}>
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${PLOT}`}
        preserveAspectRatio="none"
        className="w-full text-[hsl(var(--chart-1))]"
        style={{ height, ...plotStyle }}
        aria-hidden="true"
      >
        {hover && <rect x={hover.index * SLOT} width={SLOT} y={0} height={PLOT} fill="currentColor" opacity={0.08} data-testid="headline-bars-cursor" />}
        <line x1={0} x2={width} y1={PLOT - 0.5} y2={PLOT - 0.5} stroke="hsl(var(--border))" strokeWidth={1} vectorEffect="non-scaling-stroke" />
        {values.map((v, index) => {
          if (v <= 0) return null;
          const h = Math.max(2, (v / max) * (PLOT - 4));
          return <rect key={index} x={index * SLOT + 3} width={4} y={PLOT - h} height={h} rx={1} fill="currentColor" opacity={hover?.index === index ? 1 : 0.7} />;
        })}
      </svg>
      <figcaption className="flex justify-between text-meta tabular-nums text-ink-muted" aria-hidden="true">
        <span>{timestamps[0] !== undefined ? fmt(timestamps[0]) : ""}</span>
        <span>Now</span>
      </figcaption>
      <span id={hintId} className="sr-only">
        Left and right arrow keys read each bucket.
      </span>
      <span className="sr-only" aria-live="polite" aria-atomic="true">
        {hover?.via === "keyboard" && time ? describeSample(time, rows) : ""}
      </span>
      {hover && time && <ChartTooltip anchor={hover.anchor} time={time} rows={rows} />}
    </figure>
  );
}
