// Copyright (c) 2026 Reliant Labs

/**
 * The ML tab's telemetry reads, over the console's observability hooks
 * (hooks/observability-queries.ts) — the same ObservabilityService and
 * AlertingService reads every telemetry view makes, filtered to an ML
 * subject by the attributes its telemetry carries
 * (services/controlPlane/ml/telemetry.ts):
 *
 *   useMlSeries   metrics (QueryPanel), as the chart kit's MlSeries, on the
 *                 step axis when asked and the trainer reports its step
 *   useMlEvents   ML event records (SearchLogs over `event.name: ml.*`), as MlEvent
 *   useMlAlerts   the org's pending and firing alerts on the subject (ListAlerts)
 *   useMlScope    the subject as an ObsScope, for Observability's own views
 *
 * Each answers like an ML read (MlQuery: an MlOutcome under React Query's
 * fields), so <MlRead> draws its loading and failure states.
 */

import { useMemo } from "react";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import type { UseQueryResult } from "@tanstack/react-query";

import { MlAxis, MlCollection, MlEventSchema, MlSeriesSchema, MlSeverity, MlSubjectKind, type MlEvent, type MlEventKind, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { useAlerts, usePanelQuery, useSearchLogs } from "@/hooks/observability-queries";
import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { LogRecord, ObsScope, Panel, PanelResult } from "@/services/controlPlane/observability/types";
import { presetCovering, type RangePreset } from "@/services/controlPlane/observability/window";
import {
  alertMatches,
  EVENT_NAME_ATTR,
  eventKindOfName,
  eventQuery,
  filterKey,
  isFamily,
  ML_ATTR,
  ML_EVENT_ATTR,
  ML_GROUP_BY,
  ML_STEP_KEY,
  mlMetricName,
  subjectFilters,
  subjectKindOfName,
  type MlSignal,
  type SubjectRef,
} from "@/services/controlPlane/ml/telemetry";

import { useMlGate, type MlQuery } from "../MlParts";

export type { MlQuery };

/** A query's answer as an ML read's: the data, once there is some. */
function asMl<D, T>(query: Pick<UseQueryResult<D, unknown>, "data" | "error" | "isLoading" | "isFetching" | "refetch">, data: T | undefined): MlQuery<T> {
  return {
    data: data === undefined ? undefined : { kind: "ok", data },
    error: query.error,
    isLoading: query.isLoading,
    isFetching: query.isFetching,
    refetch: query.refetch as unknown as MlQuery<T>["refetch"],
  };
}

/** The data of an ML read, or undefined (loading, failed, not entitled, no data). */
export const okData = <T,>(q: Pick<MlQuery<T>, "data">): T | undefined => (q.data?.kind === "ok" ? q.data.data : undefined);

/** The smallest window that covers everything since `sinceMs` (the console reads presets, never two timestamps). */
export function rangeSince(sinceMs: number | null | undefined, now = Date.now()): RangePreset {
  return sinceMs ? presetCovering(Math.max(0, now - sinceMs)) : "24h";
}

/** `subject` as an ObsScope for one signal: the gate's org, every environment, the subject's attributes. */
export function useMlScope(subject: SubjectRef, signal: MlSignal): ObsScope {
  const { orgId } = useMlGate();
  const filtersKey = `${subject.kind}:${subject.id}:${signal}`;
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => ({ orgId, envIds: [], filters: subjectFilters(subject, signal) }), [orgId, filtersKey]);
}

// ── Series ────────────────────────────────────────────────────────────────────

/** What a training chart plots against: the optimizer step (where the trainer reports it), or time. */
export type MlXAxis = "step" | "time";

/** A run page's `?axis`: step unless it says time. */
export const xAxisOf = (axis: string | undefined): MlXAxis => (axis === "time" ? "time" : "step");

export interface MlSeriesOptions {
  /** A series per member: "rank", "attempt", "device", "node", "pool", "replica", "revision". */
  groupBy?: string;
  /** "step" also reads ML_STEP_KEY and plots every series against it (onStepAxis); default time. */
  axis?: MlXAxis;
  /** Percentile series per key ("p50", "p95", "p99"); none reads the mean. */
  stats?: readonly string[];
  range?: RangePreset;
  enabled?: boolean;
}

const QUANTILE: Record<string, number> = { p5: 0.05, p25: 0.25, p50: 0.5, p75: 0.75, p90: 0.9, p95: 0.95, p99: 0.99 };

/** The metrics panel a series read asks QueryPanel for: one series per key (and stat), each labelled with its key. */
export function seriesPanel(keys: readonly string[], opts: Pick<MlSeriesOptions, "groupBy" | "stats"> = {}): Panel {
  const stats = opts.stats?.length ? opts.stats : [""];
  const groupBy = [...(opts.groupBy ? [filterKey(ML_GROUP_BY[opts.groupBy] ?? opts.groupBy, "metrics")] : []), ...(keys.some(isFamily) ? [filterKey(ML_ATTR.key, "metrics")] : [])];
  return {
    id: `ml:${keys.join(",")}:${opts.groupBy ?? ""}:${stats.join(",")}`,
    title: "",
    kind: "line",
    signal: "metrics",
    series: keys.flatMap((key) =>
      stats.map((stat) => ({ label: stat ? `${key} ${stat}` : key, field: mlMetricName(key), agg: stat ? "quantile" : "avg", ...(stat ? { quantile: QUANTILE[stat] ?? 0.5 } : {}) })),
    ),
    groupBy,
  };
}

/** How one group-by value reads as a series' group: "rank 37", "attempt 2", or the value itself. */
function groupLabel(attr: string, value: string): string {
  if (attr === ML_ATTR.rank) return `rank ${value}`;
  if (attr === ML_ATTR.attempt) return `attempt ${value}`;
  return value;
}

/**
 * A QueryPanel answer as the chart kit's series: each labelled with its key
 * (and stat), its group from the group-by values. A key nothing reported is
 * a NOT_COLLECTED series, with the server's reason when it gave one.
 */
export function seriesOfPanel(result: PanelResult, keys: readonly string[], stats: readonly string[] = []): MlSeries[] {
  const absent = new Map<string, string>();
  for (const w of result.warnings) {
    const m = /^metric_absent:\s*([^:\s]+)(?::\s*(.*))?$/.exec(w);
    if (m) absent.set(m[1], m[2] ?? "");
  }
  const out: MlSeries[] = [];
  const seen = new Set<string>();
  for (const s of result.series) {
    const [key, stat = ""] = splitLabel(s.label, keys, stats);
    if (!key) continue;
    const meta = result.seriesMeta.find((m) => m.label === s.label);
    let seriesKey = key;
    const groups: string[] = [];
    let attempt = 0;
    for (const [field, value] of Object.entries(s.group)) {
      const attr = field.replace(/^(resource|metric)\.attr\./, "");
      if (attr === ML_ATTR.key) seriesKey = value;
      else {
        groups.push(groupLabel(attr, value));
        if (attr === ML_ATTR.attempt) attempt = Number(value) || 0;
      }
    }
    seen.add(key);
    out.push(
      create(MlSeriesSchema, {
        key: seriesKey,
        label: s.label,
        unit: meta?.valueUnit ?? result.unit,
        axis: MlAxis.TIME,
        x: s.timestamps,
        y: s.values.map((v) => (v === null ? NaN : v)),
        group: groups.join(" · ") || stat,
        stat,
        attempt,
        resolution: result.stepSeconds,
        collection: MlCollection.COLLECTED,
      }),
    );
  }
  for (const key of keys) {
    if (seen.has(key)) continue;
    const reason = absent.get(mlMetricName(key)) ?? "";
    const na = /^not applicable:\s*/i.exec(reason);
    out.push(create(MlSeriesSchema, { key, label: key, axis: MlAxis.TIME, collection: na ? MlCollection.NOT_APPLICABLE : MlCollection.NOT_COLLECTED, missingSource: na ? reason.slice(na[0].length) : reason }));
  }
  return out;
}

/** x of every point of `mapping` (time → step), at time `t`, linearly between its points; NaN outside it. */
function stepAt(times: readonly number[], steps: readonly number[], t: number): number {
  if (times.length === 0 || t < times[0] || t > times[times.length - 1]) return NaN;
  let lo = 0;
  let hi = times.length - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (times[mid] <= t) lo = mid;
    else hi = mid;
  }
  if (times[hi] === times[lo]) return steps[lo];
  return Math.round(steps[lo] + ((steps[hi] - steps[lo]) * (t - times[lo])) / (times[hi] - times[lo]));
}

/**
 * Time-axis series re-plotted on the step axis: each point's x is the step
 * the trainer was on at that point's time, read from the ML_STEP_KEY series
 * of the same group (an attempt's own steps, so a resume overlays the steps
 * it redid), else from all of them in time order. Null when nothing in
 * `series` reports the step: the caller stays on time. The step series
 * themselves are not returned.
 */
export function onStepAxis(series: readonly MlSeries[]): MlSeries[] | null {
  const steps = series.filter((s) => s.key === ML_STEP_KEY && s.collection === MlCollection.COLLECTED && s.x.length > 0);
  if (steps.length === 0) return null;
  const byGroup = new Map(steps.map((s) => [s.group, s]));
  const all = steps.flatMap((s) => s.x.map((x, i) => [x, s.y[i]] as const)).sort((a, b) => a[0] - b[0]);
  const merged = { x: all.map((p) => p[0]), y: all.map((p) => p[1]) };
  return series
    .filter((s) => s.key !== ML_STEP_KEY)
    .map((s) => {
      if (s.collection !== MlCollection.COLLECTED) return create(MlSeriesSchema, { ...s, axis: MlAxis.STEP });
      const m = byGroup.get(s.group) ?? merged;
      const x: number[] = [];
      const y: number[] = [];
      s.x.forEach((t, i) => {
        const step = stepAt(m.x, m.y, t);
        if (!Number.isFinite(step)) return;
        x.push(step);
        y.push(s.y[i]);
      });
      return create(MlSeriesSchema, { ...s, axis: MlAxis.STEP, x, y });
    });
}

/** Two series reads as one: loading while either is, failed when either failed, the series of both. */
export function mergeSeries(a: MlQuery<{ series: MlSeries[] }>, b: MlQuery<{ series: MlSeries[] }>): MlQuery<{ series: MlSeries[] }> {
  const da = okData(a);
  const db = okData(b);
  return {
    data: da && db ? { kind: "ok", data: { series: [...da.series, ...db.series] } } : undefined,
    error: a.error ?? b.error,
    isLoading: a.isLoading || b.isLoading,
    isFetching: a.isFetching || b.isFetching,
    refetch: (async () => {
      await Promise.all([a.refetch(), b.refetch()]);
      return a.refetch();
    }) as MlQuery<{ series: MlSeries[] }>["refetch"],
  };
}

/** A series label back to its key and stat ("serve.ttft p95" → ["serve.ttft", "p95"]). */
function splitLabel(label: string, keys: readonly string[], stats: readonly string[]): [string, string] | [] {
  if (keys.includes(label)) return [label, ""];
  for (const stat of stats) {
    const key = label.endsWith(` ${stat}`) ? label.slice(0, -(stat.length + 1)) : "";
    if (keys.includes(key)) return [key, stat];
  }
  return [];
}

/**
 * Metrics for `subject`: one QueryPanel read of `keys` (ML_ATTR-filtered),
 * as MlSeries on the time axis. With `stats` a key reads its percentiles,
 * one series each, grouped by stat.
 */
export function useMlSeries(subject: SubjectRef, keys: readonly string[], opts: MlSeriesOptions = {}): MlQuery<{ series: MlSeries[] }> {
  const scope = useMlScope(subject, "metrics");
  const statsKey = (opts.stats ?? []).join(",");
  const step = opts.axis === "step";
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const read = useMemo(() => (step ? [...keys, ML_STEP_KEY] : [...keys]), [keys.join(","), step]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const panel = useMemo(() => seriesPanel(read, { groupBy: opts.groupBy, stats: opts.stats }), [read, opts.groupBy, statsKey]);
  const query = usePanelQuery(scope, opts.range ?? "24h", panel, [], [], { enabled: opts.enabled ?? true });
  const series = useMemo(() => {
    if (!query.data) return undefined;
    const all = seriesOfPanel(query.data, read, opts.stats);
    // On the step axis where the trainer reports its step; otherwise time, without the step series.
    return { series: (step ? onStepAxis(all) : null) ?? all.filter((s) => s.key !== ML_STEP_KEY || keys.includes(ML_STEP_KEY)) };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query.data, read, statsKey, step]);
  return asMl(query, series);
}

/** Series with their groups renamed (a GPU's UUID → "GPU 3"); a group `names` lacks keeps its own. */
export function relabel(series: readonly MlSeries[], names: ReadonlyMap<string, string>): MlSeries[] {
  return series.map((s) => (names.has(s.group) ? create(MlSeriesSchema, { ...s, group: names.get(s.group)! }) : s));
}

// ── Events ────────────────────────────────────────────────────────────────────

const SEVERITY: Record<string, MlSeverity> = { info: MlSeverity.INFO, warning: MlSeverity.WARNING, critical: MlSeverity.CRITICAL };
const SEVERITY_OF_LOG: Partial<Record<LogRecord["severity"], MlSeverity>> = { warn: MlSeverity.WARNING, error: MlSeverity.CRITICAL, fatal: MlSeverity.CRITICAL, info: MlSeverity.INFO };

/** One ML event record (a log record whose `event.name` is `ml.<kind>`) as the MlEvent the event lists and chart markers draw. */
export function eventOfRecord(r: LogRecord): MlEvent {
  const a = { ...r.resource, ...r.attributes };
  const related = (() => {
    try {
      return JSON.parse(a[ML_EVENT_ATTR.related] ?? "[]") as { kind: string; id: string; label?: string }[];
    } catch {
      return [];
    }
  })();
  const endMs = Number(a[ML_EVENT_ATTR.endMs]);
  const known = new Set<string>([EVENT_NAME_ATTR, ...Object.values(ML_EVENT_ATTR)]);
  return create(MlEventSchema, {
    id: r.id,
    at: timestampFromMs(r.timeMs),
    endAt: Number.isFinite(endMs) && endMs > 0 ? timestampFromMs(endMs) : undefined,
    kind: eventKindOfName(a[EVENT_NAME_ATTR]),
    severity: SEVERITY[a[ML_EVENT_ATTR.severity] ?? ""] ?? SEVERITY_OF_LOG[r.severity] ?? MlSeverity.INFO,
    title: r.body,
    detail: a[ML_EVENT_ATTR.detail] ?? "",
    subject: a[ML_EVENT_ATTR.subjectId] !== undefined ? { kind: subjectKindOfName(a[ML_EVENT_ATTR.subjectKind]), id: a[ML_EVENT_ATTR.subjectId], label: a[ML_EVENT_ATTR.subjectLabel] ?? "" } : undefined,
    related: related.map((s) => ({ kind: subjectKindOfName(s.kind), id: s.id, label: s.label ?? "" })),
    step: BigInt(Math.max(0, Math.round(Number(a[ML_EVENT_ATTR.step]) || 0))),
    source: a[ML_EVENT_ATTR.source] ?? r.service,
    hookEventType: a[ML_EVENT_ATTR.hookEventType] ?? "",
    attributes: Object.fromEntries(Object.entries(r.attributes).filter(([k]) => !known.has(k))),
  });
}

export interface MlEventsOptions {
  /** Only these kinds; none is every ML event. */
  kinds?: readonly MlEventKind[];
  range?: RangePreset;
  enabled?: boolean;
}

/** The ML events on `subject` (and, for the org, every ML event), newest first. */
export function useMlEvents(subject: SubjectRef, opts: MlEventsOptions = {}): MlQuery<{ events: MlEvent[] }> {
  const scope = useMlScope(subject, "logs");
  const query = eventQuery(opts.kinds);
  const filters = useMemo(() => ({ range: opts.range ?? "7d", query, services: [], severities: [] }), [opts.range, query]);
  const read = useSearchLogs(opts.enabled === false ? null : scope, filters, false);
  const events = useMemo(() => (read.data ? { events: read.data.pages.flatMap((p) => p.records).map(eventOfRecord) } : undefined), [read.data]);
  return asMl(read, events);
}

// ── Alerts ────────────────────────────────────────────────────────────────────

/** The org's pending and firing alerts on `subject` (the org: every ML alert), by their labels. */
export function useMlAlerts(subject: SubjectRef): MlQuery<{ alerts: AlertInstance[] }> {
  const { orgId } = useMlGate();
  const read = useAlerts(orgId);
  const id = `${subject.kind}:${subject.id}`;
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const alerts = useMemo(() => (read.data ? { alerts: read.data.filter((a) => alertMatches(a.labels, subject)) } : undefined), [read.data, id]);
  return asMl(read, alerts);
}

/** The org, as the subject of an org-wide read. */
export const ORG_SUBJECT: SubjectRef = { kind: MlSubjectKind.ORG, id: "" };
