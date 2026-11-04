// Copyright (c) 2025 Reliant Labs

/**
 * The metrics explorer's fixtures: a small catalog covering every kind and
 * group (a declared counter, an up-down counter, a latency histogram, an
 * exponential histogram, a summary, a gauge, runtime and infra metrics, and a
 * declared metric nothing has emitted), one metric detail, and exemplars, plus
 * the PanelResult a metrics panel returns (series_meta, series_total, a partial
 * last point and a gap). Deterministic: the same call always returns the same
 * answer, so a test and a __preview__ agree.
 */

import type {
  ListMetricCatalogResult,
  MetricCatalogEntry,
  MetricDetail,
  MetricExemplar,
  MetricKind,
  SeriesMeta,
} from "@/services/controlPlane/observability/metricsTypes";
import { catalogGroup } from "@/services/controlPlane/observability/metricsTypes";
import type { Panel, PanelResult, TimeWindow } from "@/services/controlPlane/observability/types";

const MINUTE = 60_000;

interface CatalogSpec {
  name: string;
  kind: MetricKind;
  unit?: string;
  description?: string;
  monotonic?: boolean;
  temporality?: MetricCatalogEntry["temporality"];
  services?: string[];
  series?: number;
  declared?: boolean;
  seen?: boolean;
  stability?: string;
  queryable?: boolean;
}

function entry(spec: CatalogSpec, nowMs: number): MetricCatalogEntry {
  const seen = spec.seen ?? true;
  const services = spec.services ?? ["api"];
  const declared = spec.declared ?? false;
  return {
    name: spec.name,
    kind: spec.kind,
    unit: spec.unit ?? "",
    description: spec.description ?? "",
    monotonic: spec.monotonic ?? false,
    temporality: spec.temporality ?? (spec.kind === "gauge" || spec.kind === "summary" ? "unknown" : "cumulative"),
    services,
    serviceCount: services.length,
    lastSeenMs: seen ? nowMs - MINUTE : null,
    seriesEstimate: seen ? (spec.series ?? 4) : 0,
    queryable: spec.queryable ?? true,
    builtin: /^(rpc|go|forge)\./.test(spec.name),
    declared,
    seen,
    stability: spec.stability ?? (declared ? "stable" : ""),
    group: catalogGroup(spec.name, declared),
  };
}

const CATALOG: CatalogSpec[] = [
  { name: "orders.created", kind: "sum", unit: "{order}", monotonic: true, description: "Orders accepted at checkout.", declared: true, services: ["api", "worker"], series: 6 },
  { name: "orders.in_flight", kind: "sum", unit: "{order}", monotonic: false, description: "Orders being processed right now.", declared: true, series: 3 },
  { name: "orders.refunds", kind: "sum", unit: "{order}", monotonic: true, description: "Refunds issued.", declared: true, seen: false },
  { name: "checkout.latency", kind: "histogram", unit: "s", description: "Time from cart to confirmation.", declared: true, series: 12 },
  { name: "rpc.server.duration", kind: "histogram", unit: "s", description: "RPC duration.", declared: true, services: ["api", "worker", "web"], series: 48 },
  { name: "rpc.server.calls", kind: "sum", unit: "{call}", monotonic: true, description: "Total RPC calls received.", declared: true, services: ["api", "worker", "web"], series: 48 },
  { name: "go.memory.used", kind: "sum", unit: "By", monotonic: false, description: "Memory used by the Go runtime.", declared: true, series: 4 },
  { name: "go.goroutine.count", kind: "sum", unit: "{goroutine}", monotonic: false, description: "Count of live goroutines.", declared: true, series: 2 },
  { name: "queue.depth", kind: "gauge", unit: "{job}", description: "Jobs waiting in the queue.", series: 2 },
  { name: "http.client.duration", kind: "exp_histogram", unit: "ms", description: "Outbound request duration.", series: 20, queryable: true },
  { name: "db.pool.wait", kind: "summary", unit: "ms", description: "Time spent waiting for a connection.", series: 2, queryable: true },
  { name: "k8s.container.cpu.usage", kind: "gauge", unit: "{cpu}", services: [], series: 14 },
  { name: "k8s.container.memory.working_set", kind: "gauge", unit: "By", services: [], series: 14 },
];

/** The whole fixture catalog, for a window ending at `nowMs`. */
export function fixtureMetricCatalog(nowMs: number = Date.now(), name = "", limit = 0): ListMetricCatalogResult {
  const needle = name.trim().toLowerCase();
  let metrics = CATALOG.map((spec) => entry(spec, nowMs)).filter((m) => !needle || m.name.toLowerCase().includes(needle));
  const truncated = limit > 0 && metrics.length > limit;
  if (limit > 0) metrics = metrics.slice(0, limit);
  return { metrics, truncated };
}

export function fixtureMetricEntry(name: string, nowMs: number = Date.now()): MetricCatalogEntry | undefined {
  const spec = CATALOG.find((s) => s.name === name);
  return spec ? entry(spec, nowMs) : undefined;
}

/** One metric's detail: attributes with cardinality (one enumerated, one high-cardinality) and suggested breakdowns. */
export function fixtureMetricDetail(name: string, nowMs: number = Date.now()): MetricDetail | null {
  const info = fixtureMetricEntry(name, nowMs);
  if (!info) return null;
  return {
    info,
    intervalSeconds: 60,
    mixedTemporality: false,
    attributes: [
      { key: "service.name", resource: true, distinctEstimate: info.serviceCount, enumerated: [], highCardinality: false },
      { key: "status", resource: false, distinctEstimate: 3, enumerated: ["ok", "error", "timeout"], highCardinality: false },
      { key: "rpc.method", resource: false, distinctEstimate: 18, enumerated: [], highCardinality: false },
      { key: "user.id", resource: false, distinctEstimate: 4200, enumerated: [], highCardinality: true },
    ],
    suggestedBreakdowns: ["status", "rpc.method", "service.name"],
  };
}

/** Exemplars of a latency histogram: three whose trace is stored (first), two sampled out. */
export function fixtureMetricExemplars(nowMs: number = Date.now(), limit = 20): MetricExemplar[] {
  const make = (i: number, traceStored: boolean): MetricExemplar => ({
    timeMs: nowMs - (i + 1) * 4 * MINUTE,
    value: 0.9 + i * 0.35,
    traceId: (0xa1b2c3d4 + i).toString(16).padStart(32, "0"),
    spanId: (0x1000 + i).toString(16).padStart(16, "0"),
    attributes: { "rpc.method": i % 2 ? "Checkout" : "ListOrders", status: i === 4 ? "error" : "ok" },
    traceStored,
  });
  return [make(0, true), make(1, true), make(2, true), make(3, false), make(4, false)].slice(0, limit);
}

/**
 * What QueryPanel returns for a metrics line panel over the fixture catalog:
 * one series per panel series with its SeriesMeta, a partial last bucket, and —
 * when the panel has a formula — a gap in the first bucket (a missing operand).
 */
export function fixtureMetricPanel(panel: Panel, window: TimeWindow, nowMs: number = Date.now()): PanelResult {
  const step = 60;
  const buckets = Math.max(2, Math.min(60, Math.floor((window.endMs - window.startMs) / (step * 1000))));
  const timestamps = Array.from({ length: buckets }, (_, i) => Math.floor(window.startMs / 1000) + i * step);
  const partialFrom = Math.floor((nowMs - MINUTE) / 1000);
  const seriesMeta: SeriesMeta[] = [];
  const series = panel.series.map((intent, index) => {
    const info = fixtureMetricEntry(intent.field ?? "", nowMs);
    const kind = info?.kind ?? "gauge";
    const agg = intent.agg;
    seriesMeta.push({
      label: intent.label,
      metric: intent.field ?? "",
      kind,
      agg,
      quantile: agg === "quantile" ? intent.quantile : undefined,
      valueUnit: agg === "rate" ? `${info?.unit ?? ""}/s` : agg === "count" ? "{count}" : (info?.unit ?? ""),
    });
    const values = timestamps.map((_, i) => (panel.formula && i === 0 ? null : Math.round((2 + index + Math.sin(i / 4)) * 1000) / 1000));
    return {
      label: intent.label,
      group: {} as Record<string, string>,
      timestamps,
      values,
      partial: timestamps.map((t) => t >= partialFrom),
      value: null,
    };
  });
  return {
    kind: panel.kind,
    unit: panel.unit ?? "",
    series,
    columns: [],
    rows: [],
    stepSeconds: step,
    truncated: false,
    funnelSteps: [],
    issues: [],
    sessions: [],
    seriesMeta,
    seriesTotal: series.length,
    warnings: [],
    compiledSql: [],
  };
}
