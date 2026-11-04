// Copyright (c) 2025 Reliant Labs

/**
 * The latency scatter's and histogram's arithmetic, apart from React so it can
 * be tested as plain functions: the points a trace list plots, the two scales
 * (time, and latency on a linear or log axis), their ticks, the histogram's
 * buckets, the percentiles, and the brushed region.
 *
 * A point is one trace: when its root span started, how long the root span
 * took (the request as its caller saw it — the list's Duration column), and
 * whether any of its spans failed (the list's Status column). Same three facts
 * the list shows, so a point and its row can never disagree.
 */

import type { TraceSummary } from "@/services/controlPlane/observability/types";

import { operationName } from "../traceTitle";

export interface TracePoint {
  traceId: string;
  startMs: number;
  durationMs: number;
  error: boolean;
  /** "api GetOrder": what a tooltip and an accessible name say. */
  label: string;
}

export type LatencyScale = "linear" | "log";

/** The brushed region: a time span and a latency span, both inclusive. */
export interface TraceRegion {
  startMs: number;
  endMs: number;
  minMs: number;
  maxMs: number;
}

export function pointsOf(traces: readonly TraceSummary[]): TracePoint[] {
  return traces.map((t) => ({
    traceId: t.traceId,
    startMs: t.startMs,
    durationMs: Math.max(0, t.rootDurationMs),
    error: t.errorCount > 0,
    label: [t.rootService || "unknown", operationName(t.rootOperation) || t.traceId].join(" "),
  }));
}

/** A one-dimensional scale: a domain mapped onto a pixel range, and back. */
export interface Scale {
  domain: [number, number];
  toPx: (value: number) => number;
  fromPx: (px: number) => number;
}

export function linearScale(domain: [number, number], range: [number, number]): Scale {
  const [d0, d1] = domain;
  const [r0, r1] = range;
  const span = d1 - d0 || 1;
  return {
    domain,
    toPx: (v) => r0 + ((v - d0) / span) * (r1 - r0),
    fromPx: (px) => d0 + ((px - r0) / (r1 - r0 || 1)) * span,
  };
}

/**
 * A log10 scale. A value at or below the domain's floor (a 0ms span) is drawn
 * ON the floor rather than off the chart: it is a real trace, and hiding it
 * would make the scatter disagree with the list.
 */
export function logScale(domain: [number, number], range: [number, number]): Scale {
  const [d0, d1] = domain;
  const l0 = Math.log10(d0);
  const l1 = Math.log10(d1);
  const inner = linearScale([l0, l1], range);
  return {
    domain,
    toPx: (v) => inner.toPx(Math.log10(Math.max(v, d0))),
    fromPx: (px) => 10 ** inner.fromPx(px),
  };
}

/** The smallest latency a log axis shows: 10µs. Anything faster sits on the floor. */
const LOG_FLOOR_MS = 0.01;

/**
 * The latency axis' domain. Linear starts at zero (a bar or a dot's height
 * then means its size); log runs between the decades around the data, so the
 * body and the tail are both readable.
 */
export function latencyDomain(durations: readonly number[], scale: LatencyScale): [number, number] {
  let max = 0;
  let minPositive = Infinity;
  for (const d of durations) {
    if (d > max) max = d;
    if (d > 0 && d < minPositive) minPositive = d;
  }
  if (scale === "linear") return [0, max > 0 ? niceCeil(max * 1.05) : 1];
  if (!Number.isFinite(minPositive)) return [1, 10];
  const lo = Math.max(LOG_FLOOR_MS, 10 ** Math.floor(Math.log10(minPositive)));
  let hi = 10 ** Math.ceil(Math.log10(max));
  if (hi <= lo) hi = lo * 10;
  return [lo, hi];
}

/** 1, 2, 2.5, 5 times a power of ten: the round number at or above `v`. */
export function niceCeil(v: number): number {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

/** About `want` evenly spaced round values across a linear domain, both ends included when round. */
export function linearTicks([lo, hi]: [number, number], want = 5): number[] {
  if (!(hi > lo)) return [lo];
  const rough = (hi - lo) / want;
  const p = 10 ** Math.floor(Math.log10(rough));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * p).find((s) => s >= rough) ?? 10 * p;
  const ticks: number[] = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi + step * 1e-9; v += step) ticks.push(Number(v.toPrecision(12)));
  return ticks;
}

/** The decades of a log domain; with fewer than three, the 2s and 5s between them too. */
export function logTicks([lo, hi]: [number, number]): number[] {
  const first = Math.ceil(Math.log10(lo) - 1e-9);
  const last = Math.floor(Math.log10(hi) + 1e-9);
  const decades: number[] = [];
  for (let e = first; e <= last; e++) decades.push(10 ** e);
  if (decades.length >= 3) return decades;
  const out: number[] = [];
  for (let e = first - 1; e <= last; e++) {
    for (const m of [1, 2, 5]) {
      const v = m * 10 ** e;
      if (v >= lo * (1 - 1e-9) && v <= hi * (1 + 1e-9)) out.push(Number(v.toPrecision(12)));
    }
  }
  return out;
}

const TIME_STEPS_MS = [
  1_000, 5_000, 10_000, 15_000, 30_000, 60_000, 120_000, 300_000, 600_000, 900_000, 1_800_000, 3_600_000, 7_200_000,
  10_800_000, 21_600_000, 43_200_000, 86_400_000,
];

/** Round wall-clock instants across [lo, hi], about `want` of them, and the step between them. */
export function timeTicks([lo, hi]: [number, number], want = 5): { ticks: number[]; stepMs: number } {
  const rough = Math.max(1, (hi - lo) / want);
  const stepMs = TIME_STEPS_MS.find((s) => s >= rough) ?? Math.ceil(rough / 86_400_000) * 86_400_000;
  // Align to the local clock (14:00, not 13:47), which an epoch multiple is
  // only in UTC-offset-whole-hours zones; the offset of `lo` is close enough
  // for every tick of one chart.
  const offset = new Date(lo).getTimezoneOffset() * 60_000;
  const ticks: number[] = [];
  for (let t = Math.ceil((lo - offset) / stepMs) * stepMs + offset; t <= hi; t += stepMs) ticks.push(t);
  return { ticks, stepMs };
}

/** "14:05", "14:05:30", or "Oct 25" — as precise as the tick step needs. */
export function formatTimeTick(ms: number, stepMs: number): string {
  const date = new Date(ms);
  if (stepMs >= 86_400_000) return date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return date.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    ...(stepMs < 60_000 ? { second: "2-digit" } : {}),
    hour12: false,
  });
}

export interface HistogramBucket {
  /** Inclusive lower bound, ms. */
  lo: number;
  /** Exclusive upper bound, ms (inclusive for the last bucket). */
  hi: number;
  ok: number;
  error: number;
}

/**
 * Latency buckets across the axis' domain — evenly spaced on the axis, so on a
 * log axis each covers the same RATIO (Datadog's latency distribution) and on
 * a linear one the same width. Each bucket counts its successes and failures
 * apart, for the stacked bars.
 */
export function histogram(points: readonly TracePoint[], domain: [number, number], scale: LatencyScale, count = 24): HistogramBucket[] {
  const [lo, hi] = domain;
  const edges: number[] = [];
  for (let i = 0; i <= count; i++) {
    const f = i / count;
    edges.push(scale === "log" ? 10 ** (Math.log10(lo) + f * (Math.log10(hi) - Math.log10(lo))) : lo + f * (hi - lo));
  }
  const buckets: HistogramBucket[] = edges.slice(0, -1).map((edge, i) => ({ lo: edge, hi: edges[i + 1], ok: 0, error: 0 }));
  const position = (d: number) =>
    scale === "log"
      ? (Math.log10(Math.max(d, lo)) - Math.log10(lo)) / (Math.log10(hi) - Math.log10(lo))
      : (d - lo) / (hi - lo || 1);
  for (const p of points) {
    const i = Math.min(count - 1, Math.max(0, Math.floor(position(p.durationMs) * count)));
    if (p.error) buckets[i].error++;
    else buckets[i].ok++;
  }
  return buckets;
}

export interface Percentiles {
  p50: number;
  p95: number;
  p99: number;
}

/** Nearest-rank percentiles of the plotted traces' latency; null with nothing plotted. */
export function percentiles(points: readonly TracePoint[]): Percentiles | null {
  if (points.length === 0) return null;
  const sorted = points.map((p) => p.durationMs).sort((a, b) => a - b);
  const at = (q: number) => sorted[Math.min(sorted.length - 1, Math.max(0, Math.ceil(q * sorted.length) - 1))];
  return { p50: at(0.5), p95: at(0.95), p99: at(0.99) };
}

export function inRegion(point: TracePoint, region: TraceRegion): boolean {
  return (
    point.startMs >= region.startMs &&
    point.startMs <= region.endMs &&
    point.durationMs >= region.minMs &&
    point.durationMs <= region.maxMs
  );
}

/** A region from two corners in any order. */
export function regionOf(a: { timeMs: number; latencyMs: number }, b: { timeMs: number; latencyMs: number }): TraceRegion {
  return {
    startMs: Math.min(a.timeMs, b.timeMs),
    endMs: Math.max(a.timeMs, b.timeMs),
    minMs: Math.max(0, Math.min(a.latencyMs, b.latencyMs)),
    maxMs: Math.max(a.latencyMs, b.latencyMs),
  };
}

/**
 * The plotted point nearest a pixel, within `radius` pixels; failures win a
 * tie, since they are drawn on top. Null when none is that close.
 */
export function nearestPoint(
  points: readonly TracePoint[],
  x: Scale,
  y: Scale,
  px: number,
  py: number,
  radius = 8
): TracePoint | null {
  let best: TracePoint | null = null;
  let bestD = radius * radius;
  for (const p of points) {
    const dx = x.toPx(p.startMs) - px;
    const dy = y.toPx(p.durationMs) - py;
    const d = dx * dx + dy * dy;
    if (d < bestD || (d === bestD && p.error && best && !best.error)) {
      best = p;
      bestD = d;
    }
  }
  return best;
}

/**
 * The time span the x axis covers. With every matching trace loaded, it is
 * the window asked for. With only the newest pages loaded, it starts at the
 * oldest loaded trace: stretching it to the window's start would draw an empty
 * stretch that looks like "no traffic" when it means "not loaded".
 */
export function timeDomain(points: readonly TracePoint[], window: { startMs: number; endMs: number }, complete: boolean): [number, number] {
  if (complete || points.length === 0) return [window.startMs, window.endMs];
  let oldest = Infinity;
  for (const p of points) if (p.startMs < oldest) oldest = p.startMs;
  const lo = Math.min(oldest, window.endMs);
  const pad = Math.max(1_000, (window.endMs - lo) * 0.02);
  return [Math.max(window.startMs, lo - pad), window.endMs];
}
