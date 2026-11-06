// Copyright (c) 2025 Reliant Labs

/**
 * The traces page's latency chart: every plotted trace as one dot — when it
 * started across, how long its root span took up — blue when it succeeded and
 * red when any span failed, with the latency distribution beside it as a
 * histogram that shares the scatter's latency axis (Honeycomb's heatmap,
 * Datadog APM's and Jaeger's scatter).
 *
 * ── WHAT IT PLOTS ───────────────────────────────────────────────────────────
 *
 * Exactly the traces the list below has loaded, so a dot and its row cannot
 * disagree, and the chart costs no query of its own. The header says how many
 * of the matching traces that is; "Load older" under the list plots more.
 *
 * ── INTERACTION ─────────────────────────────────────────────────────────────
 *
 * Click a dot to open its trace. Drag a rectangle to select a time and latency
 * range: the list narrows to the traces inside it, and the dots outside it
 * dim. Both are pointer enhancements over the list, which stays the keyboard
 * and screen-reader path to every trace (the figure's caption says so).
 *
 * ── DRAWING ─────────────────────────────────────────────────────────────────
 *
 * SVG, coloured with the theme's semantic tokens as classes (`fill-info` — blue
 * in every scheme, where pure-black's `primary` is ink — and `fill-danger`), so every colour scheme and light/dark mode is right with no
 * palette to read. The dots are memoized apart from the hover and brush state,
 * so moving the pointer re-renders a tooltip, not every dot.
 */

import { memo, useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type RefObject } from "react";
import { AlertCircle } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { cn } from "@/lib/utils";

import { formatCount, formatDuration, formatTimestamp } from "../format";
import {
  formatTimeTick,
  histogram,
  inRegion,
  latencyDomain,
  linearScale,
  linearTicks,
  logScale,
  logTicks,
  nearestPoint,
  percentiles,
  regionOf,
  timeDomain,
  timeTicks,
  type LatencyScale,
  type Percentiles,
  type Scale,
  type TracePoint,
  type TraceRegion,
} from "./traceScatterModel";

/** Plot geometry, CSS pixels. The scatter and histogram share TOP and PLOT_HEIGHT, so their latency axes line up. */
const TOP = 10;
const PLOT_HEIGHT = 200;
const BOTTOM = 24;
const HEIGHT = TOP + PLOT_HEIGHT + BOTTOM;
const SCATTER_LEFT = 52;
const SCATTER_RIGHT = 10;
/** Room right of the bars for the percentile labels ("p99 1.53s"). */
const HIST_RIGHT = 84;
/** Width used before the first measurement (and under jsdom, which measures nothing). */
const FALLBACK_WIDTH = 640;
/** A drag shorter than this is a click. */
const DRAG_THRESHOLD = 4;
const DOT_RADIUS = 3;

export type TraceLatencyStatus = "loading" | "error" | "ready";

export interface TraceLatencyChartProps {
  status: TraceLatencyStatus;
  points: readonly TracePoint[];
  /** The window the list asked for; null before the first page. */
  window: { startMs: number; endMs: number } | null;
  /** Every matching trace is loaded (no older page). */
  complete: boolean;
  /** How many traces matched in the window (the server's count), when known. */
  total: number | null;
  /** True when the list is filtered: the empty state says so. */
  filtered: boolean;
  region: TraceRegion | null;
  onRegion: (region: TraceRegion | null) => void;
  onOpen: (traceId: string) => void;
}

export function TraceLatencyChart(props: TraceLatencyChartProps) {
  const [scale, setScale] = useState<LatencyScale>("log");
  const { status, points } = props;
  const ready = status === "ready" && points.length > 0 && props.window;
  return (
    <Card padding="none" className="overflow-hidden" data-testid="obs-trace-latency">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-border px-4 py-2.5">
        <div className="min-w-0">
          <h3 className="text-sm font-medium text-ink">Latency</h3>
          <p className="text-xs text-ink-muted" data-testid="obs-trace-latency-coverage">
            {coverage(props)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Legend />
          <div role="group" aria-label="Latency axis" className="inline-flex rounded-lg border border-border bg-surface-sunken p-0.5">
            {(["linear", "log"] as const).map((s) => (
              <button
                key={s}
                type="button"
                aria-pressed={s === scale}
                onClick={() => setScale(s)}
                data-testid={`obs-trace-latency-scale-${s}`}
                className={cn(
                  "rounded-md px-2.5 py-1 text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:min-h-[44px]",
                  s === scale ? "bg-surface font-medium text-ink shadow-sm" : "text-ink-muted hover:text-ink"
                )}
              >
                {s === "linear" ? "Linear" : "Log"}
              </button>
            ))}
          </div>
        </div>
      </div>
      {status === "loading" ? (
        <LoadingPlot />
      ) : status === "error" ? (
        <PlotMessage tone="error" title="The latency chart could not be drawn" detail="The traces did not load. The error, and a way to retry, are below." />
      ) : !ready ? (
        <PlotMessage
          title="No traces to plot"
          detail={props.filtered ? "Nothing in this window matches the filters." : "Nothing was traced in this window."}
        />
      ) : (
        <Plot {...props} window={props.window!} scale={scale} />
      )}
      {/* Always laid out, shown only over a plot, so the card keeps one height through loading, empty and data. */}
      <p className={cn("border-t border-border px-4 py-1.5 text-2xs text-ink-muted", !ready && "invisible")} aria-hidden="true">
        Drag across the plot to narrow the list to a time and latency range. Click a dot to open its trace.
      </p>
    </Card>
  );
}

/** "Plotting the 100 newest of 1,204 traces" / "Plotting all 40 traces". */
function coverage({ status, points, complete, total }: TraceLatencyChartProps): string {
  if (status === "loading") return "Loading traces…";
  if (status === "error" || points.length === 0) return "One dot per trace: start time against root-span latency";
  const n = formatCount(points.length);
  if (complete) return points.length === 1 ? "Plotting the one matching trace" : `Plotting all ${n} matching traces`;
  const of = total && total > points.length ? ` of ${formatCount(total)}` : "";
  return `Plotting the ${n} newest${of} traces. Load older traces below to plot more.`;
}

function Legend() {
  return (
    <ul className="flex items-center gap-3 text-xs text-ink-muted" aria-label="Legend">
      <li className="flex items-center gap-1.5">
        <span className="h-2 w-2 rounded-full bg-info" aria-hidden="true" />
        OK
      </li>
      <li className="flex items-center gap-1.5">
        <span className="h-2 w-2 rounded-full bg-danger" aria-hidden="true" />
        Error
      </li>
    </ul>
  );
}

/** The chart's frame while the first page loads: the plot's own height, so nothing below moves when it arrives. */
function LoadingPlot() {
  return (
    <div className="flex gap-3 p-3" style={{ height: HEIGHT }} aria-busy="true" aria-label="Loading the latency chart" data-testid="obs-trace-latency-loading">
      <div className="h-full flex-1 animate-pulse rounded-md bg-border/40 motion-reduce:animate-none" />
      <div className="h-full w-32 animate-pulse rounded-md bg-border/40 motion-reduce:animate-none sm:w-52" />
    </div>
  );
}

/** Empty and error: a message where the plot would be, at the plot's height, over faint gridlines. */
function PlotMessage({ title, detail, tone }: { title: string; detail: string; tone?: "error" }) {
  return (
    <div className="relative flex items-center justify-center px-6" style={{ height: HEIGHT }} data-testid={tone === "error" ? "obs-trace-latency-error" : "obs-trace-latency-empty"}>
      <div className="pointer-events-none absolute inset-x-4 inset-y-6 flex flex-col justify-between" aria-hidden="true">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="border-t border-dashed border-border/70" />
        ))}
      </div>
      <div role={tone === "error" ? "status" : undefined} className="relative max-w-sm rounded-md bg-surface px-3 py-2 text-center">
        <p className={cn("flex items-center justify-center gap-1.5 text-sm font-medium", tone === "error" ? "text-danger-ink" : "text-ink")}>
          {tone === "error" && <AlertCircle className="h-4 w-4 shrink-0" aria-hidden="true" />}
          {title}
        </p>
        <p className="mt-0.5 text-xs text-ink-muted">{detail}</p>
      </div>
    </div>
  );
}

function useWidth<T extends HTMLElement>(): [RefObject<T | null>, number] {
  const ref = useRef<T | null>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setWidth(el.clientWidth);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  return [ref, width];
}

type Hover = { point: TracePoint; x: number; y: number };
type Drag = { x0: number; y0: number; x1: number; y1: number; moved: boolean };

function Plot({
  points,
  window,
  complete,
  region,
  onRegion,
  onOpen,
  scale,
}: TraceLatencyChartProps & { window: { startMs: number; endMs: number }; scale: LatencyScale }) {
  const [scatterRef, measured] = useWidth<HTMLDivElement>();
  const width = measured || FALLBACK_WIDTH;
  const plotWidth = Math.max(40, width - SCATTER_LEFT - SCATTER_RIGHT);

  const x = useMemo(() => linearScale(timeDomain(points, window, complete), [0, plotWidth]), [points, window, complete, plotWidth]);
  const yDomain = useMemo(() => latencyDomain(points.map((p) => p.durationMs), scale), [points, scale]);
  const y = useMemo(
    () => (scale === "log" ? logScale(yDomain, [PLOT_HEIGHT, 0]) : linearScale(yDomain, [PLOT_HEIGHT, 0])),
    [yDomain, scale]
  );
  const pct = useMemo(() => percentiles(points), [points]);
  const buckets = useMemo(() => histogram(points, yDomain, scale), [points, yDomain, scale]);

  const [hover, setHover] = useState<Hover | null>(null);
  // The drag lives in a ref, read and written by the pointer handlers, so a
  // burst of moves between renders never acts on a stale copy; the state
  // twin only draws it.
  const dragRef = useRef<Drag | null>(null);
  const [drag, setDragState] = useState<Drag | null>(null);
  const setDrag = (next: Drag | null) => {
    dragRef.current = next;
    setDragState(next);
  };

  /** Pointer position inside the plot area, clamped to it. */
  const local = (event: ReactPointerEvent<SVGRectElement>) => {
    const box = event.currentTarget.getBoundingClientRect();
    return {
      x: Math.min(plotWidth, Math.max(0, event.clientX - box.left)),
      y: Math.min(PLOT_HEIGHT, Math.max(0, event.clientY - box.top)),
    };
  };
  const onPointerDown = (event: ReactPointerEvent<SVGRectElement>) => {
    if (event.button !== 0) return;
    const p = local(event);
    // Keep the drag when the pointer leaves the plot. Capture is an
    // enhancement: a pointer the browser no longer tracks throws, and the
    // brush still works without it.
    try {
      event.currentTarget.setPointerCapture?.(event.pointerId);
    } catch {
      /* no capture */
    }
    setDrag({ x0: p.x, y0: p.y, x1: p.x, y1: p.y, moved: false });
    setHover(null);
  };
  const onPointerMove = (event: ReactPointerEvent<SVGRectElement>) => {
    const p = local(event);
    const current = dragRef.current;
    if (current) {
      const moved = current.moved || Math.hypot(p.x - current.x0, p.y - current.y0) > DRAG_THRESHOLD;
      setDrag({ ...current, x1: p.x, y1: p.y, moved });
      return;
    }
    const near = nearestPoint(points, x, y, p.x, p.y);
    setHover(near ? { point: near, x: x.toPx(near.startMs), y: y.toPx(near.durationMs) } : null);
  };
  const onPointerUp = (event: ReactPointerEvent<SVGRectElement>) => {
    const current = dragRef.current;
    if (!current) return;
    const p = local(event);
    setDrag(null);
    if (current.moved || Math.hypot(p.x - current.x0, p.y - current.y0) > DRAG_THRESHOLD) {
      onRegion(
        regionOf({ timeMs: x.fromPx(current.x0), latencyMs: y.fromPx(current.y0) }, { timeMs: x.fromPx(p.x), latencyMs: y.fromPx(p.y) })
      );
      return;
    }
    const near = nearestPoint(points, x, y, p.x, p.y);
    if (near) onOpen(near.traceId);
  };

  const errors = points.reduce((n, p) => n + (p.error ? 1 : 0), 0);
  const caption =
    `${formatCount(points.length)} traces plotted by start time and root-span latency, ${formatCount(errors)} with errors.` +
    (pct ? ` Median ${formatDuration(pct.p50)}, p95 ${formatDuration(pct.p95)}, p99 ${formatDuration(pct.p99)}.` : "") +
    " The trace list below has the same traces; open one from its root operation.";

  const brush = drag?.moved
    ? { x: Math.min(drag.x0, drag.x1), y: Math.min(drag.y0, drag.y1), w: Math.abs(drag.x1 - drag.x0), h: Math.abs(drag.y1 - drag.y0) }
    : region
      ? regionBox(region, x, y, plotWidth)
      : null;

  return (
    <figure className="m-0" data-testid="obs-trace-latency-plot">
      <figcaption className="sr-only">{caption}</figcaption>
      {/* The histogram keeps a fixed column (its bars plus HIST_RIGHT for the
          percentile labels) beside the scatter at every width, so the two
          share one latency axis even on a phone. */}
      <div className="grid grid-cols-[minmax(0,1fr)_8.5rem] sm:grid-cols-[minmax(0,1fr)_13rem]">
        <div ref={scatterRef} className="relative min-w-0">
          <svg width={width} height={HEIGHT} className="block select-none" aria-hidden="true" data-testid="obs-trace-scatter">
            <g transform={`translate(${SCATTER_LEFT},${TOP})`}>
              <LatencyGrid y={y} scale={scale} domain={yDomain} width={plotWidth} />
              <TimeAxis x={x} width={plotWidth} />
              <PercentileRules pct={pct} y={y} width={plotWidth} />
              <Dots points={points} x={x} y={y} region={region} />
              {brush && (
                <rect
                  x={brush.x}
                  y={brush.y}
                  width={Math.max(1, brush.w)}
                  height={Math.max(1, brush.h)}
                  className="fill-primary/10 stroke-primary"
                  strokeWidth={1}
                  strokeDasharray={drag ? "3 2" : undefined}
                  data-testid="obs-trace-latency-brush"
                />
              )}
              {hover && !drag && (
                <circle cx={hover.x} cy={hover.y} r={DOT_RADIUS + 3} className="fill-none stroke-ink" strokeWidth={1.5} />
              )}
              <rect
                width={plotWidth}
                height={PLOT_HEIGHT}
                fill="transparent"
                className={hover && !drag ? "cursor-pointer" : "cursor-crosshair"}
                /* pan-y: a vertical swipe still scrolls the page on a phone. */
                style={{ touchAction: "pan-y" }}
                onPointerDown={onPointerDown}
                onPointerMove={onPointerMove}
                onPointerUp={onPointerUp}
                onPointerCancel={() => setDrag(null)}
                onPointerLeave={() => !drag && setHover(null)}
                data-testid="obs-trace-latency-surface"
              />
            </g>
          </svg>
          {hover && !drag && <Tooltip hover={hover} plotWidth={plotWidth} />}
        </div>
        <LatencyHistogram buckets={buckets} y={y} pct={pct} />
      </div>
    </figure>
  );
}

function regionBox(region: TraceRegion, x: Scale, y: Scale, plotWidth: number) {
  const x0 = Math.max(0, x.toPx(region.startMs));
  const x1 = Math.min(plotWidth, x.toPx(region.endMs));
  const yTop = Math.max(0, y.toPx(region.maxMs));
  const yBottom = Math.min(PLOT_HEIGHT, y.toPx(region.minMs));
  return { x: x0, y: yTop, w: Math.max(0, x1 - x0), h: Math.max(0, yBottom - yTop) };
}

/** The dots: successes first, failures over them, so a red dot is never hidden under a blue one. */
const Dots = memo(function Dots({ points, x, y, region }: { points: readonly TracePoint[]; x: Scale; y: Scale; region: TraceRegion | null }) {
  const ordered = useMemo(() => [...points.filter((p) => !p.error), ...points.filter((p) => p.error)], [points]);
  return (
    <g data-testid="obs-trace-latency-dots">
      {ordered.map((p) => {
        const dim = region !== null && !inRegion(p, region);
        return (
          <circle
            key={p.traceId}
            cx={x.toPx(p.startMs)}
            cy={y.toPx(p.durationMs)}
            r={DOT_RADIUS}
            className={p.error ? "fill-danger" : "fill-info"}
            fillOpacity={dim ? 0.15 : p.error ? 0.85 : 0.6}
            data-trace-id={p.traceId}
            data-status={p.error ? "error" : "ok"}
          />
        );
      })}
    </g>
  );
});

function LatencyGrid({ y, scale, domain, width }: { y: Scale; scale: LatencyScale; domain: [number, number]; width: number }) {
  const ticks = scale === "log" ? logTicks(domain) : linearTicks(domain, 4);
  return (
    <g>
      {ticks.map((t) => (
        <g key={t} transform={`translate(0,${y.toPx(t)})`}>
          <line x2={width} className="stroke-border" strokeOpacity={0.7} />
          <text x={-6} dy="0.32em" textAnchor="end" className="fill-ink-muted text-2xs tabular-nums">
            {formatDuration(t)}
          </text>
        </g>
      ))}
    </g>
  );
}

function TimeAxis({ x, width }: { x: Scale; width: number }) {
  const { ticks, stepMs } = timeTicks(x.domain, Math.max(2, Math.floor(width / 110)));
  return (
    <g transform={`translate(0,${PLOT_HEIGHT})`}>
      <line x2={width} className="stroke-border" />
      {ticks.map((t) => {
        const at = x.toPx(t);
        // A label at either end is anchored inward, so it is not clipped by the plot's edge.
        const anchor = at < 24 ? "start" : at > width - 24 ? "end" : "middle";
        return (
          <text key={t} x={at} y={16} textAnchor={anchor} className="fill-ink-muted text-2xs tabular-nums">
            {formatTimeTick(t, stepMs)}
          </text>
        );
      })}
    </g>
  );
}

const PCT_KEYS = ["p50", "p95", "p99"] as const;

function PercentileRules({ pct, y, width }: { pct: Percentiles | null; y: Scale; width: number }) {
  if (!pct) return null;
  return (
    <g aria-hidden="true">
      {PCT_KEYS.map((k) => (
        <line key={k} x2={width} y1={y.toPx(pct[k])} y2={y.toPx(pct[k])} className="stroke-ink-muted" strokeOpacity={0.55} strokeDasharray="4 3" />
      ))}
    </g>
  );
}

/**
 * Percentile labels pushed apart so p95 and p99 stay legible when they are
 * close: each sits at its rule, or the label's height below the one above it.
 */
export function spreadLabels(positions: readonly number[], gap = 11): number[] {
  const order = positions.map((p, i) => ({ p, i })).sort((a, b) => a.p - b.p);
  const out = new Array<number>(positions.length);
  let last = -Infinity;
  for (const { p, i } of order) {
    const at = Math.max(p, last + gap);
    out[i] = at;
    last = at;
  }
  return out;
}

function LatencyHistogram({ buckets, y, pct }: { buckets: ReturnType<typeof histogram>; y: Scale; pct: Percentiles | null }) {
  const [ref, measured] = useWidth<HTMLDivElement>();
  const width = measured || 176;
  const barSpace = Math.max(20, width - HIST_RIGHT - 4);
  const max = Math.max(1, ...buckets.map((b) => b.ok + b.error));
  // 13px apart: a text-2xs label is 10px tall, and at 11 p95 and p99 touched when close.
  const labelY = pct ? spreadLabels(PCT_KEYS.map((k) => y.toPx(pct[k])), 13) : [];
  return (
    <div ref={ref} className="min-w-0 border-l border-border" data-testid="obs-trace-latency-histogram">
      <svg width={width} height={HEIGHT} className="block" aria-hidden="true">
        <g transform={`translate(4,${TOP})`}>
          {buckets.map((b) => {
            const top = y.toPx(b.hi);
            const h = Math.max(1, y.toPx(b.lo) - top - 1);
            const okW = (b.ok / max) * barSpace;
            const errW = (b.error / max) * barSpace;
            if (b.ok + b.error === 0) return null;
            return (
              <g key={b.lo} data-testid="obs-trace-latency-bucket" data-ok={b.ok} data-error={b.error}>
                {b.ok > 0 && <rect x={0} y={top} width={okW} height={h} className="fill-info" fillOpacity={0.55} />}
                {b.error > 0 && <rect x={okW} y={top} width={errW} height={h} className="fill-danger" fillOpacity={0.8} />}
              </g>
            );
          })}
          {pct &&
            PCT_KEYS.map((k, i) => {
              const at = y.toPx(pct[k]);
              return (
                <g key={k} data-testid={`obs-trace-latency-${k}`}>
                  <line x2={barSpace} y1={at} y2={at} className="stroke-ink-muted" strokeDasharray="4 3" />
                  <text x={barSpace + 4} y={labelY[i]} dy="0.32em" className="fill-ink text-2xs tabular-nums">
                    <tspan className="fill-ink-muted">{k} </tspan>
                    {formatDuration(pct[k])}
                  </text>
                </g>
              );
            })}
          <line y1={PLOT_HEIGHT} y2={PLOT_HEIGHT} x2={barSpace} className="stroke-border" />
          <text x={0} y={PLOT_HEIGHT + 16} className="fill-ink-muted text-2xs">
            Traces per bucket
          </text>
        </g>
      </svg>
    </div>
  );
}

function Tooltip({ hover, plotWidth }: { hover: Hover; plotWidth: number }) {
  const left = SCATTER_LEFT + hover.x;
  const flip = hover.x > plotWidth * 0.6;
  return (
    <div
      className="pointer-events-none absolute z-10 w-60 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs shadow-md"
      style={{ left, top: TOP + hover.y + 10, transform: flip ? "translateX(calc(-100% - 8px))" : "translateX(8px)" }}
      data-testid="obs-trace-latency-tooltip"
    >
      <p className="truncate font-mono text-ink">{hover.point.label}</p>
      <p className="mt-0.5 flex items-center justify-between gap-2 tabular-nums text-ink-muted">
        <span>{formatTimestamp(hover.point.startMs)}</span>
        <span className="font-medium text-ink">{formatDuration(hover.point.durationMs)}</span>
      </p>
      <p className={cn("mt-0.5", hover.point.error ? "text-danger-ink" : "text-ink-muted")}>{hover.point.error ? "Failed" : "OK"} · click to open</p>
    </div>
  );
}
