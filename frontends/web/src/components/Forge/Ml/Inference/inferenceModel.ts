// Copyright (c) 2026 Reliant Labs

/**
 * What the Inference pages draw, as pure functions of the contract's
 * messages: the list's header numbers, a revision's SLI deltas, the Changes
 * comparison side by side, cold start as bar segments, the KV-per-replica
 * heatmap and the slow-request list's filters.
 */

import {
  MlCollection,
  type MlArmMetric,
  type MlChangesComparison,
  type MlInferenceService,
  type MlRevision,
  type MlSeries,
  type MlSliDelta,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import type { HeatmapRow, MlTone } from "../kit";
import { has } from "../kit/format";

// ── Services list ─────────────────────────────────────────────────────────────

export interface ServicesKpis {
  services: number;
  rps: number;
  worstSlo?: MlInferenceService;
  worstTtft?: MlInferenceService;
  /** Cost-weighted over the generative services: Σ$ / Σtokens. */
  blendedUsdPerMillion: number;
  gpus: number;
  /** GPU-weighted busy ratio. */
  busy: number;
  usd24h: number;
}

export function servicesKpis(services: readonly MlInferenceService[]): ServicesKpis {
  const rps = services.reduce((s, x) => s + (has(x.rps) ? x.rps : 0), 0);
  const slo = services.filter((s) => has(s.sloAttainment));
  const worstSlo = [...slo].sort((a, b) => a.sloAttainment - b.sloAttainment)[0];
  const worstTtft = [...services.filter((s) => has(s.ttftP95Ms))].sort((a, b) => b.ttftP95Ms - a.ttftP95Ms)[0];
  // $/1M = $/h ÷ (tok/s·3600/1e6), so tokens/h = $/h ÷ ($/1M) × 1e6.
  let usd = 0;
  let millions = 0;
  for (const s of services) {
    if (!has(s.usdPerMillionTokens) || s.usdPerMillionTokens <= 0 || !s.cost) continue;
    usd += s.cost.usd;
    millions += s.cost.usd / s.usdPerMillionTokens;
  }
  const gpus = services.reduce((sum, s) => sum + s.accelerators, 0);
  const busy = gpus ? services.reduce((sum, s) => sum + (has(s.busyRatio) ? s.busyRatio * s.accelerators : 0), 0) / gpus : NaN;
  return { services: services.length, rps, worstSlo, worstTtft, blendedUsdPerMillion: millions ? usd / millions : NaN, gpus, busy, usd24h: services.reduce((s, x) => s + (x.cost?.usd ?? 0), 0) };
}

/** Below target is a warning; below target by more than the budget is danger. */
export function sloTone(attainment: number, target: number): MlTone {
  if (!has(attainment) || !has(target)) return "neutral";
  if (attainment >= target) return "success";
  return target - attainment > 0.005 ? "danger" : "warning";
}

export const kvTone = (kv: number): MlTone | undefined => (!has(kv) ? undefined : kv >= 0.9 ? "danger" : kv >= 0.8 ? "warning" : undefined);

// ── Revisions ─────────────────────────────────────────────────────────────────

/** A delta as a relative change: (after − before) / before. */
export const relDelta = (d: Pick<MlSliDelta, "before" | "after">) => (d.before ? (d.after - d.before) / Math.abs(d.before) : 0);

/** "+4.1%", "−23%", "±0%". */
export function fmtDelta(ratio: number, digits?: number): string {
  if (!has(ratio)) return "—";
  const pct = ratio * 100;
  const d = digits ?? (Math.abs(pct) < 10 ? 1 : 0);
  if (Math.abs(pct) < 0.05) return "±0%";
  return `${pct > 0 ? "+" : "−"}${Math.abs(pct).toFixed(d)}%`;
}

/** A delta's dot: a significant regression is a warning, a significant improvement success, the rest neutral. */
export function deltaTone(d: Pick<MlSliDelta, "significant" | "regression">): MlTone {
  if (!d.significant) return "neutral";
  return d.regression ? "warning" : "success";
}

/** Live revisions first (most traffic first), then ended ones (newest first). */
export function orderRevisions(revisions: readonly MlRevision[]): MlRevision[] {
  const live = revisions.filter((r) => !r.endedAt).sort((a, b) => b.trafficShare - a.trafficShare);
  const ended = revisions.filter((r) => !!r.endedAt).sort((a, b) => Number(b.endedAt!.seconds) - Number(a.endedAt!.seconds));
  return [...live, ...ended];
}

/** The current revision's delta for an SLI label ("TTFT p95"). */
export const deltaFor = (rev: MlRevision | undefined, label: string) => rev?.deltas.find((d) => d.label === label);

// ── Changes ───────────────────────────────────────────────────────────────────

export interface ArmMetricRow {
  key: string;
  label: string;
  unit: string;
  control: MlArmMetric;
  arms: { arm: string; metric: MlArmMetric }[];
  /** Low and high across every interval: a shared scale for the CI bars. */
  lo: number;
  hi: number;
}

/** One row per metric: the control's value and each other arm's, on one scale. */
export function armMetricRows(c: MlChangesComparison | undefined): ArmMetricRow[] {
  if (!c) return [];
  const control = c.arms.find((a) => a.arm === c.controlArm) ?? c.arms[0];
  if (!control) return [];
  const others = c.arms.filter((a) => a !== control);
  return control.metrics.map((m) => {
    const arms = others.flatMap((a) => {
      const metric = a.metrics.find((x) => x.key === m.key);
      return metric ? [{ arm: a.arm, metric }] : [];
    });
    const all = [m, ...arms.map((a) => a.metric)];
    const lo = Math.min(...all.map((x) => (has(x.ciLow) ? x.ciLow : x.value)));
    const hi = Math.max(...all.map((x) => (has(x.ciHigh) ? x.ciHigh : x.value)));
    const pad = (hi - lo) * 0.15 || Math.abs(hi) * 0.05 || 1;
    return { key: m.key, label: m.label, unit: m.unit, control: m, arms, lo: lo - pad, hi: hi + pad };
  });
}

/** An arm's difference from the control: points for a ratio ("+0.4pt"), relative otherwise ("−14%"). */
export function armDeltaLabel(row: Pick<ArmMetricRow, "unit" | "control">, arm: MlArmMetric): string {
  if (row.unit === "ratio") {
    const pt = (arm.value - row.control.value) * 100;
    if (Math.abs(pt) < 0.05) return "±0pt";
    return `${pt > 0 ? "+" : "−"}${Math.abs(pt).toFixed(1)}pt`;
  }
  return fmtDelta(arm.deltaRatio);
}

/** An arm metric's verdict: better, worse, or no significant difference. */
export function armVerdict(m: MlArmMetric): { label: string; tone: MlTone } {
  if (!m.significant) return { label: "no significant difference", tone: "neutral" };
  return m.better ? { label: "better", tone: "success" } : { label: "worse", tone: "warning" };
}

/** Days since a comparison started (for "running 3 days"). */
export const daysSince = (startMs: number | null, now: number) => (startMs ? Math.max(0, Math.round((now - startMs) / 86_400_000)) : 0);

// ── Charts ────────────────────────────────────────────────────────────────────

/** A series nothing can report for this subject (Triton has no TTFT): its reason, or null. */
export function notApplicable(series: readonly MlSeries[]): string | null {
  if (series.length === 0) return null;
  const na = series.every((s) => s.collection === MlCollection.NOT_APPLICABLE);
  return na ? series[0].missingSource || "Not applicable to this service" : null;
}

/** The series of one key (in a multi-key response). */
export const byKey = (series: readonly MlSeries[], key: string) => series.filter((s) => s.key === key);

/**
 * One heatmap row per series (per replica), averaged into `bucket` columns:
 * 288 five-minute points read better as 96 quarter-hours.
 */
export function heatmapRows(series: readonly MlSeries[], bucket = 3, hrefOf?: (s: MlSeries) => string | undefined, hot?: (s: MlSeries) => boolean): { rows: HeatmapRow[]; columns: string[] } {
  const first = series.find((s) => s.x.length > 0);
  if (!first) return { rows: [], columns: [] };
  const cols = Math.ceil(first.x.length / bucket);
  const columns = Array.from({ length: cols }, (_, c) => {
    const d = new Date(first.x[Math.min(first.x.length - 1, c * bucket)] * 1000);
    return d.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false });
  });
  const rows = series.map((s) => ({
    id: s.subject?.id || s.group,
    label: s.group || s.subject?.label || s.key,
    cells: Array.from({ length: cols }, (_, c) => {
      const vals = s.y.slice(c * bucket, (c + 1) * bucket).filter(Number.isFinite);
      return vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : null;
    }),
    tone: hot?.(s) ? ("warning" as const) : undefined,
    href: hrefOf?.(s),
  }));
  return { rows, columns };
}

/** The highest finite value of a series set (a peak to call out). */
export function peakOf(series: readonly MlSeries[]): { value: number; series?: MlSeries } {
  let best = { value: NaN } as { value: number; series?: MlSeries };
  for (const s of series)
    for (const y of s.y)
      if (Number.isFinite(y) && !(y <= best.value)) {
        best = { value: y, series: s };
      }
  return best;
}
