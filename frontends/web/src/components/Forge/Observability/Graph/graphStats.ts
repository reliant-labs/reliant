// Copyright (c) 2026 Reliant Labs
import type { GraphEdge } from "@/services/controlPlane/observability/graphTypes";
import { formatCount, formatDuration, formatPercent } from "../format";

export type Histogram = NonNullable<GraphEdge["duration"]>;

/** A histogram whose buckets line up with its bounds (one per bound plus +Inf). */
export function usableHistogram(h: GraphEdge["duration"]): h is Histogram {
  return !!h && h.version === 1 && h.buckets.length === h.boundsSeconds.length + 1 && h.count > 0n;
}

/**
 * The q-quantile in milliseconds from noncumulative buckets, interpolated
 * linearly inside the bucket that holds it (Prometheus histogram_quantile).
 * A quantile in the +Inf bucket reports the last finite bound: "at least".
 */
export function histogramQuantile(h: GraphEdge["duration"], q: number): number | undefined {
  if (!usableHistogram(h)) return undefined;
  const total = Number(h.count);
  const rank = q * total;
  let seen = 0;
  for (let i = 0; i < h.buckets.length; i++) {
    const n = Number(h.buckets[i]);
    if (n > 0 && seen + n >= rank) {
      if (i >= h.boundsSeconds.length) return h.boundsSeconds[h.boundsSeconds.length - 1] * 1000;
      const lower = i === 0 ? 0 : h.boundsSeconds[i - 1];
      const upper = h.boundsSeconds[i];
      return (lower + (upper - lower) * ((rank - seen) / n)) * 1000;
    }
    seen += n;
  }
  return undefined;
}

/** Sums histograms with identical bounds; a mismatch yields undefined rather than mixed buckets. */
export function sumHistograms(hs: readonly GraphEdge["duration"][]): Histogram | undefined {
  let out: Histogram | undefined;
  for (const h of hs) {
    if (!usableHistogram(h)) continue;
    if (!out) { out = { ...h, buckets: [...h.buckets] }; continue; }
    if (out.boundsSeconds.length !== h.boundsSeconds.length || out.boundsSeconds.some((b, i) => b !== h.boundsSeconds[i])) return undefined;
    out = { ...out, count: out.count + h.count, sumSeconds: out.sumSeconds + h.sumSeconds, buckets: out.buckets.map((b, i) => b + h.buckets[i]) };
  }
  return out;
}

export const fmtCount = (n: bigint) => formatCount(Number(n));
export const fmtLatency = (ms: number | undefined) => (ms === undefined ? "—" : formatDuration(ms));
export function fmtErrorRate(faults: bigint, total: bigint): string {
  return total > 0n ? formatPercent(Number(faults) / Number(total)) : "—";
}
/** Error fraction for colouring: 0 when nothing was observed. */
export const errorFraction = (faults: bigint, total: bigint) => (total > 0n ? Number(faults) / Number(total) : 0);

/** "reliant.v1.ChatService" → "ChatService": the name a reader scans for. */
export function shortService(service: string): string {
  return service.split(".").at(-1) || service;
}
