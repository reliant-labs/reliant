// Copyright (c) 2025 Reliant Labs

/**
 * The metrics explorer's RPCs — ObservabilityService.ListMetrics (the catalog),
 * GetMetric (the detail pane) and ListMetricExemplars — mapped into the
 * console's model (./metricsTypes.ts). The QueryPanel half of the explorer
 * (series_meta, series_total, warnings, partial and null points) is mapped in
 * api.ts, next to the rest of QueryPanel.
 *
 * int64 arrives as bigint and is read with Number(); an enum value this build
 * does not know (a newer server) reads as "unknown", never undefined.
 */

import {
  MetricTemporality as WireTemporality,
  MetricType as WireMetricType,
  ObservabilityService,
  type MetricAttribute as WireMetricAttribute,
  type MetricExemplar as WireMetricExemplar,
  type MetricInfo as WireMetricInfo,
} from "@/gen/controlplane/services/observability/v1/observability_pb";

import { obsClient } from "./obsClient";
import { kindOfWire, msOf, wireScope } from "./api";
import type {
  GetMetricRequest,
  ListMetricCatalogRequest,
  ListMetricCatalogResult,
  ListMetricExemplarsRequest,
  MetricAttribute,
  MetricCatalogEntry,
  MetricDetail,
  MetricExemplar,
  MetricKind,
  MetricTemporality,
} from "./metricsTypes";
import { catalogGroup } from "./metricsTypes";
import type { ObsScope, TimeWindow } from "./types";

function client() {
  return obsClient(ObservabilityService);
}

const WIRE_KIND: Partial<Record<MetricKind, WireMetricType>> = {
  gauge: WireMetricType.GAUGE,
  sum: WireMetricType.SUM,
  histogram: WireMetricType.HISTOGRAM,
  exp_histogram: WireMetricType.EXPONENTIAL_HISTOGRAM,
  summary: WireMetricType.SUMMARY,
};

function temporalityOf(t: WireTemporality): MetricTemporality {
  return t === WireTemporality.DELTA ? "delta" : t === WireTemporality.CUMULATIVE ? "cumulative" : "unknown";
}

export function mapMetricInfo(m: WireMetricInfo): MetricCatalogEntry {
  return {
    name: m.name,
    kind: kindOfWire(m.type),
    unit: m.unit,
    description: m.description,
    monotonic: m.monotonic,
    temporality: temporalityOf(m.temporality),
    services: [...m.services],
    serviceCount: m.serviceCount,
    lastSeenMs: m.lastSeen ? msOf(m.lastSeen) : null,
    seriesEstimate: Number(m.seriesEstimate),
    queryable: m.queryable,
    builtin: m.builtin,
    declared: m.declared,
    seen: m.seen,
    stability: m.stability,
    group: catalogGroup(m.name, m.declared),
  };
}

function mapAttribute(a: WireMetricAttribute): MetricAttribute {
  return {
    key: a.key,
    resource: a.resource,
    distinctEstimate: Number(a.distinctEstimate),
    enumerated: [...a.enumerated],
    highCardinality: a.highCardinality,
  };
}

function mapExemplar(e: WireMetricExemplar): MetricExemplar {
  return {
    timeMs: msOf(e.time),
    value: e.value,
    traceId: e.traceId,
    spanId: e.spanId,
    attributes: { ...e.attributes },
    traceStored: e.traceStored,
  };
}

/** The metric catalog for the scope's window (the server caps it at 24 hours). */
export async function listMetricCatalog(req: ObsScope & { window: TimeWindow } & ListMetricCatalogRequest, options: { signal?: AbortSignal } = {}): Promise<ListMetricCatalogResult> {
  const res = await client().listMetrics({
    scope: wireScope(req, req.window),
    nameContains: req.name || undefined,
    limit: req.limit ?? 0,
  }, options);
  return { metrics: res.metrics.map(mapMetricInfo), truncated: res.truncated };
}

/** One metric's detail: its catalog entry, export interval, attribute cardinalities and suggested breakdowns. */
export async function getMetric(req: ObsScope & { window: TimeWindow } & GetMetricRequest, options: { signal?: AbortSignal } = {}): Promise<MetricDetail> {
  const res = await client().getMetric({
    scope: wireScope(req, req.window),
    metricName: req.name,
    metricType: req.kind ? WIRE_KIND[req.kind] : undefined,
  }, options);
  return {
    info: mapMetricInfo(res.info ?? ({ name: req.name } as WireMetricInfo)),
    intervalSeconds: res.intervalSeconds,
    mixedTemporality: res.mixedTemporality,
    attributes: res.attributes.map(mapAttribute),
    suggestedBreakdowns: [...res.suggestedBreakdowns],
  };
}

/** Up to 20 exemplars of one metric (a sampled observation and its trace), stored traces first. */
export async function listMetricExemplars(req: ObsScope & { window: TimeWindow } & ListMetricExemplarsRequest, options: { signal?: AbortSignal } = {}): Promise<MetricExemplar[]> {
  const range = req.range ? wireScope(req, req.range).timeRange : undefined;
  const res = await client().listMetricExemplars({
    scope: wireScope(req, req.window),
    metricName: req.name,
    metricType: req.kind ? WIRE_KIND[req.kind] : undefined,
    where: (req.where ?? []).map((p) => ({ field: p.field, op: p.op, value: p.value ?? "", values: p.values ?? [] })),
    group: req.group ?? {},
    timeRange: range,
    minValue: req.minValue,
    limit: req.limit ?? 0,
  }, options);
  return res.exemplars.map(mapExemplar);
}
