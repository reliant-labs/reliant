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

import type { Transport } from "@connectrpc/connect";
import type { MessageInitShape } from "@bufbuild/protobuf";
import {
  MetricTemporality as WireTemporality,
  MetricType as WireMetricType,
  ObservabilityService,
  type MetricAttribute as WireMetricAttribute,
  type MetricExemplar as WireMetricExemplar,
  type MetricInfo as WireMetricInfo,
  type GetMetricRequestSchema,
  type GetMetricResponse,
  type ListMetricExemplarsRequestSchema,
  type ListMetricExemplarsResponse,
  type ListMetricsRequestSchema,
  type ListMetricsResponse,
} from "@/gen/controlplane/services/observability/v1/observability_pb";

import { getControlPlaneClient } from "../client";
import { obsCall } from "./obsInterceptors";
import { kindOfWire, msOf, wireScope, wireTimeRange, type ObsWindow } from "./api";
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

function client(transport?: Transport) {
  return getControlPlaneClient(ObservabilityService, transport);
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
export function metricCatalogRequest(req: ObsScope & { window: ObsWindow } & ListMetricCatalogRequest): MessageInitShape<typeof ListMetricsRequestSchema> {
  return { scope: wireScope(req, req.window), nameContains: req.name || undefined, limit: req.limit ?? 0 };
}

export async function listMetricCatalog(req: ObsScope & { window: TimeWindow } & ListMetricCatalogRequest, options: { signal?: AbortSignal } = {}, transport?: Transport): Promise<ListMetricCatalogResult> {
  return toMetricCatalog(await client(transport).listMetrics(metricCatalogRequest(req), obsCall(options)));
}

export function toMetricCatalog(res: ListMetricsResponse): ListMetricCatalogResult {
  return { metrics: res.metrics.map(mapMetricInfo), truncated: res.truncated };
}

/** One metric's detail: its catalog entry, export interval, attribute cardinalities and suggested breakdowns. */
export function metricRequest(req: ObsScope & { window: ObsWindow } & GetMetricRequest): MessageInitShape<typeof GetMetricRequestSchema> {
  return { scope: wireScope(req, req.window), metricName: req.name, metricType: req.kind ? WIRE_KIND[req.kind] : undefined };
}

export async function getMetric(req: ObsScope & { window: TimeWindow } & GetMetricRequest, options: { signal?: AbortSignal } = {}, transport?: Transport): Promise<MetricDetail> {
  return toMetricDetail(await client(transport).getMetric(metricRequest(req), obsCall(options)), req.name);
}

/** GetMetric's answer; `name` is the metric asked for, for an answer without its catalog entry. */
export function toMetricDetail(res: GetMetricResponse, name: string): MetricDetail {
  return {
    info: mapMetricInfo(res.info ?? ({ name } as WireMetricInfo)),
    intervalSeconds: res.intervalSeconds,
    mixedTemporality: res.mixedTemporality,
    attributes: res.attributes.map(mapAttribute),
    suggestedBreakdowns: [...res.suggestedBreakdowns],
  };
}

/** Up to 20 exemplars of one metric (a sampled observation and its trace), stored traces first. `range` is the bucket asked about: absolute. */
export function metricExemplarsRequest(req: ObsScope & { window: ObsWindow } & ListMetricExemplarsRequest): MessageInitShape<typeof ListMetricExemplarsRequestSchema> {
  return {
    scope: wireScope(req, req.window),
    metricName: req.name,
    metricType: req.kind ? WIRE_KIND[req.kind] : undefined,
    where: (req.where ?? []).map((p) => ({ field: p.field, op: p.op, value: p.value ?? "", values: p.values ?? [] })),
    group: req.group ?? {},
    timeRange: req.range ? wireTimeRange(req.range) : undefined,
    minValue: req.minValue,
    limit: req.limit ?? 0,
  };
}

export async function listMetricExemplars(req: ObsScope & { window: TimeWindow } & ListMetricExemplarsRequest, options: { signal?: AbortSignal } = {}, transport?: Transport): Promise<MetricExemplar[]> {
  return toMetricExemplars(await client(transport).listMetricExemplars(metricExemplarsRequest(req), obsCall(options)));
}

export function toMetricExemplars(res: ListMetricExemplarsResponse): MetricExemplar[] {
  return res.exemplars.map(mapExemplar);
}
