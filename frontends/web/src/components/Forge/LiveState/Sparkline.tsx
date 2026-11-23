// Copyright (c) 2026 Reliant Labs

/**
 * A word-sized line: the shape of a series, no axes. For a row in a list,
 * where a chart would be too much and a number alone hides the trend.
 */

import { cn } from "@/lib/utils";

export function Sparkline({
  values,
  label,
  className,
  width = 64,
  height = 18,
}: {
  values: readonly (number | null)[];
  /** What it shows, for a screen reader: "CPU over the last 24 hours". */
  label: string;
  className?: string;
  width?: number;
  height?: number;
}) {
  const points = values.flatMap((v, i) => (v === null || !Number.isFinite(v) ? [] : [{ i, v }]));
  if (points.length < 2) {
    return <span className={cn("inline-block text-meta text-ink-muted", className)} style={{ width }} aria-label={`${label}: no samples`}>—</span>;
  }
  const max = Math.max(...points.map((p) => p.v));
  const min = Math.min(...points.map((p) => p.v));
  const span = max - min || 1;
  const last = values.length - 1 || 1;
  const d = points.map((p, n) => `${n === 0 ? "M" : "L"}${((p.i / last) * width).toFixed(1)},${(height - 2 - ((p.v - min) / span) * (height - 4)).toFixed(1)}`).join(" ");
  return (
    <svg role="img" aria-label={label} width={width} height={height} viewBox={`0 0 ${width} ${height}`} className={cn("inline-block overflow-visible stroke-primary", className)}>
      <path d={d} fill="none" strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
