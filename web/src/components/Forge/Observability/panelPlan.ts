// Copyright (c) 2025 Reliant Labs

/**
 * How one dashboard panel is answered with the RPCs control-plane offers.
 *
 * A panel (forge's obsdash spec) is a renderer-neutral INTENT — "count server
 * spans named X with status error, per time bucket" — and phase 1 of the
 * observability service has no "run this panel" RPC. So the console
 * translates, honestly:
 *
 *   overview   service-level RED (requests, errors, error rate, latency
 *              percentiles, per-bucket trends) from GetServiceOverview —
 *              exact, server-computed. Used when the panel filters by service
 *              at most.
 *   red        RED over the traces whose ROOT span matches (ListTraces' red
 *              summary, computed across the whole window). Used when a panel
 *              names rpcs (span.name), which the overview cannot filter by.
 *   rpcTable   per-rpc rows grouped in the browser from the newest traces
 *              (≤500) — a sample, and labelled as one.
 *   metric     one QueryMetric per series: metrics panels map 1:1.
 *   link       a "search" panel lists rows; the Logs and Traces views do that.
 *   none       a panel this translation cannot answer truthfully — said so,
 *              with the way into the view that can.
 *
 * A LINE panel that only `red` can answer (an rpc's latency over time) is
 * drawn as the window's numbers rather than a line: the service has no
 * per-rpc time series yet, and a line invented from a sample would be read as
 * a measurement.
 */

import type { AttributeFilter, MetricAggregation, MetricKind, Panel, Predicate, SeriesIntent } from "@/services/controlPlane/observability/types";

export type Measure = "requests" | "rate" | "errors" | "errorRate" | "p50" | "p95" | "p99";

export interface TraceScope {
  services: string[];
  /** Substring of the root span name (an rpc, or every rpc of a proto service by its prefix). */
  rootSpanName: string;
  errorsOnly: boolean;
  spanAttributes: AttributeFilter[];
}

export interface MetricPlanQuery {
  label: string;
  metricName: string;
  metricType?: MetricKind;
  aggregation?: MetricAggregation;
  groupBy: string[];
  filters: AttributeFilter[];
  services: string[];
}

export type PanelPlan =
  | { kind: "overview"; services: string[]; measures: LabelledMeasure[]; shape: "number" | "line" | "table"; byService: boolean; note?: string }
  | { kind: "red"; scope: TraceScope; measures: LabelledMeasure[]; note?: string }
  | { kind: "rpcTable"; scope: TraceScope; measures: LabelledMeasure[] }
  | { kind: "metric"; queries: MetricPlanQuery[]; shape: "number" | "line" | "table" }
  | { kind: "link"; to: "logs" | "traces"; service?: string; errorsOnly: boolean }
  | { kind: "none"; reason: string; to: "logs" | "traces"; service?: string };

export interface LabelledMeasure {
  label: string;
  measure: Measure;
}

const SERVICE = "service.name";
const ENV = "deployment.environment.name";
const LATENCY_FIELD = "span.duration";

const isError = (p: Predicate) =>
  (p.field === "span.status" || p.field === "log.severity") && p.op === "eq" && (p.value ?? "").toLowerCase() === "error";

/** service.name predicates → the scope's services filter; the env predicate is the page's own scope. */
function servicesOf(where: Predicate[]): string[] {
  const out: string[] = [];
  for (const p of where) {
    if (p.field !== SERVICE) continue;
    if (p.op === "eq" && p.value) out.push(p.value);
    if (p.op === "in" && p.values) out.push(...p.values);
  }
  return out;
}

/**
 * Trace predicates → ListTraces filters. Returns null for one the RPCs cannot
 * express (a regex on an attribute, a numeric bound), so the caller can say
 * so instead of silently dropping it.
 */
function traceScopeOf(where: Predicate[]): { scope: TraceScope; namesRpcs: boolean } | null {
  const scope: TraceScope = { services: servicesOf(where), rootSpanName: "", errorsOnly: false, spanAttributes: [] };
  let namesRpcs = false;
  for (const p of where) {
    if (p.field === SERVICE || p.field === ENV) continue;
    if (p.field === "span.kind") continue; // server spans: what both RPCs count
    if (isError(p)) {
      scope.errorsOnly = true;
      continue;
    }
    if (p.field === "span.name") {
      // The rpc-shape regex obsdash puts on every rpc panel selects server
      // spans named <pkg>.<Service>/<Method>; root server spans are exactly that.
      if (p.op === "match") continue;
      if ((p.op === "eq" || p.op === "prefix") && p.value) {
        scope.rootSpanName = p.value;
        namesRpcs = true;
        continue;
      }
      return null;
    }
    const attr = p.field.match(/^(?:span|resource)\.attr\.(.+)$/);
    if (attr && (p.op === "eq" || p.op === "neq" || p.op === "exists")) {
      scope.spanAttributes.push({ key: attr[1], op: p.op, value: p.value ?? "" });
      continue;
    }
    return null;
  }
  return { scope, namesRpcs };
}

/** A trace series → the RED measure it is, or null for one no RPC computes (count_distinct, p90…). */
function measureOf(series: SeriesIntent): Measure | null {
  if (series.agg === "count") return (series.where ?? []).some(isError) ? "errors" : "requests";
  if (series.agg === "quantile" && series.field === LATENCY_FIELD) {
    const q = series.quantile ?? 0;
    if (Math.abs(q - 0.5) < 1e-9) return "p50";
    if (Math.abs(q - 0.95) < 1e-9) return "p95";
    if (Math.abs(q - 0.99) < 1e-9) return "p99";
  }
  return null;
}

function measuresOf(panel: Panel): LabelledMeasure[] | null {
  // A ratio panel is errors / requests: one error-rate measure.
  if (panel.ratio && panel.series.length === 2) {
    const [num, den] = panel.series.map(measureOf);
    if (num === "errors" && den === "requests") return [{ label: "error rate", measure: "errorRate" }];
    return null;
  }
  const out: LabelledMeasure[] = [];
  for (const series of panel.series) {
    const measure = measureOf(series);
    if (!measure) return null;
    out.push({ label: series.label, measure });
  }
  // A panel scoped to errors counts errors, whatever its series says.
  if ((panel.where ?? []).some(isError)) {
    return out.map((m) => (m.measure === "requests" ? { ...m, measure: "errors" as const } : m));
  }
  return out;
}

const AGG: Record<string, MetricAggregation | undefined> = {
  sum: "sum",
  avg: "avg",
  max: "max",
  count: "count",
  last: "avg",
};

function metricAggregation(series: SeriesIntent): MetricAggregation | undefined | null {
  if (series.agg === "quantile") {
    const q = series.quantile ?? 0;
    if (Math.abs(q - 0.5) < 1e-9) return "p50";
    if (Math.abs(q - 0.95) < 1e-9) return "p95";
    if (Math.abs(q - 0.99) < 1e-9) return "p99";
    return null;
  }
  if (series.agg in AGG) return AGG[series.agg];
  return null;
}

function attrKey(field: string): string {
  return field.replace(/^(?:metric|resource)\.attr\./, "");
}

function planMetric(panel: Panel, extraServices: string[], shape: "number" | "line" | "table"): PanelPlan {
  const queries: MetricPlanQuery[] = [];
  for (const series of panel.series) {
    const aggregation = metricAggregation(series);
    if (aggregation === null || !series.field) {
      return { kind: "none", reason: `The "${series.label}" series uses an aggregation the metrics query doesn't offer.`, to: "traces" };
    }
    const where = [...(panel.where ?? []), ...(series.where ?? [])];
    const filters: AttributeFilter[] = [];
    for (const p of where) {
      if (p.field === SERVICE || p.field === ENV) continue;
      if (p.op !== "eq" && p.op !== "neq" && p.op !== "exists") {
        return { kind: "none", reason: `The filter on ${p.field} can't be expressed as a metric filter.`, to: "traces" };
      }
      filters.push({ key: attrKey(p.field), op: p.op, value: p.value ?? "" });
    }
    const type = series.metricType;
    queries.push({
      label: series.label,
      metricName: series.field,
      metricType: type === "sum" || type === "gauge" || type === "histogram" ? type : undefined,
      aggregation,
      groupBy: (panel.groupBy ?? []).map(attrKey),
      filters,
      services: [...servicesOf(where), ...extraServices],
    });
  }
  return { kind: "metric", queries, shape };
}

/** The plan for one panel, with the dashboard's service filter applied on top of its own. */
export function planPanel(panel: Panel, serviceFilter?: string): PanelPlan {
  const where = panel.where ?? [];
  const extra = serviceFilter ? [serviceFilter] : [];
  const linkService = serviceFilter ?? servicesOf(where)[0];
  const to: "logs" | "traces" = panel.signal === "logs" ? "logs" : "traces";
  const shape = panel.kind === "line" ? "line" : panel.kind === "table" ? "table" : "number";

  if (panel.kind === "search") {
    return { kind: "link", to, service: linkService, errorsOnly: where.some(isError) };
  }
  if (panel.signal === "metrics") return planMetric(panel, extra, shape);
  if (panel.signal !== "traces") {
    return { kind: "none", reason: "Aggregates over logs aren't available yet.", to, service: linkService };
  }

  const traced = traceScopeOf(where);
  const measures = measuresOf(panel);
  if (!traced || !measures) {
    return { kind: "none", reason: "This panel asks for a figure the observability service doesn't compute yet.", to, service: linkService };
  }
  const scope: TraceScope = { ...traced.scope, services: [...traced.scope.services, ...extra] };
  const groupBy = panel.groupBy ?? [];
  const byService = groupBy.length === 1 && groupBy[0] === SERVICE;
  const byRpc = groupBy.length === 1 && groupBy[0] === "span.name";
  const filtersBeyondService = traced.namesRpcs || scope.spanAttributes.length > 0;

  if (panel.kind === "table") {
    if (byRpc) return { kind: "rpcTable", scope, measures };
    if (byService && !filtersBeyondService) {
      return { kind: "overview", services: scope.services, measures, shape: "table", byService: true };
    }
    return { kind: "none", reason: `Grouping by ${groupBy.join(", ") || "nothing"} isn't available yet.`, to, service: linkService };
  }
  if (groupBy.length > 0 && !byService) {
    return { kind: "none", reason: `Grouping by ${groupBy.join(", ")} isn't available yet.`, to, service: linkService };
  }

  // An errors-scoped panel is still an overview answer when all it counts is
  // errors; latency OF failed requests is not, and goes to ListTraces.
  const overviewCanAnswer = !filtersBeyondService && (!scope.errorsOnly || measures.every((m) => m.measure === "errors"));
  if (overviewCanAnswer) {
    // Latency has no per-bucket trend in the overview: a latency LINE falls back to the window's percentiles.
    const latencyLine = shape === "line" && measures.some((m) => m.measure === "p50" || m.measure === "p95" || m.measure === "p99");
    return {
      kind: "overview",
      services: scope.services,
      measures,
      shape: latencyLine ? "number" : shape,
      byService,
      note: latencyLine ? "Percentiles over the whole window; latency over time isn't available yet." : undefined,
    };
  }
  return {
    kind: "red",
    scope,
    measures: measures.map((m) => (m.measure === "requests" && shape === "line" ? { ...m, measure: "rate" as const } : m)),
    note: shape === "line" ? "Over the whole window, from the traces this rpc started; per-rpc trends aren't available yet." : undefined,
  };
}
