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
 */

export function HeadlineBars({
  values,
  timestamps,
  label,
  testId,
  height = 112,
}: {
  values: readonly number[];
  /** Epoch seconds, one per bucket. */
  timestamps: readonly number[];
  /** The figure's accessible name ("Runs started, last 24 hours"). */
  label: string;
  testId?: string;
  /** CSS pixels. */
  height?: number;
}) {
  if (values.length === 0) return null;
  const max = Math.max(1, ...values);
  const width = values.length * 10;
  const plot = 100;
  const span = (timestamps[timestamps.length - 1] ?? 0) - (timestamps[0] ?? 0);
  const fmt = (sec: number) =>
    new Date(sec * 1000).toLocaleString(undefined, span <= 86_400 ? { hour: "numeric" } : { month: "short", day: "numeric" });
  return (
    <figure className="space-y-1.5" data-testid={testId}>
      <svg
        viewBox={`0 0 ${width} ${plot}`}
        preserveAspectRatio="none"
        className="w-full text-[hsl(var(--chart-1))]"
        style={{ height }}
        role="img"
        aria-label={label}
      >
        <line x1={0} x2={width} y1={plot - 0.5} y2={plot - 0.5} stroke="hsl(var(--border))" strokeWidth={1} vectorEffect="non-scaling-stroke" />
        {values.map((value, index) => {
          if (value <= 0) return null;
          const h = Math.max(2, (value / max) * (plot - 4));
          return <rect key={index} x={index * 10 + 1.5} width={7} y={plot - h} height={h} rx={1.5} fill="currentColor" opacity={0.85} />;
        })}
      </svg>
      <figcaption className="flex justify-between text-meta tabular-nums text-ink-muted" aria-hidden="true">
        <span>{timestamps[0] !== undefined ? fmt(timestamps[0]) : ""}</span>
        <span>Now</span>
      </figcaption>
    </figure>
  );
}
