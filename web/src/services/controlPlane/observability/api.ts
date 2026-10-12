// Copyright (c) 2025 Reliant Labs

/**
 * The observability RPCs — `controlplane.v1.ObservabilityService` on
 * control-plane's public listener — mapped into the console's model
 * (./types.ts). This is the only file that knows the wire shape.
 *
 * Every request carries a QueryScope: the caller's current org (verified
 * server-side against membership), this environment's control-plane id, the
 * services filter, and a time range the server caps per RPC (7 days for logs,
 * traces and the overview; 30 for metrics, profiles and dashboards). The browser sends typed requests and a
 * query STRING; it never sends SQL, and it never builds a filter out of the
 * text a reader typed.
 *
 * SearchLogs' query grammar, parsed server-side (proto: SearchLogsRequest.query):
 *
 *   timeout  "connection refused"  severity:error  severity:>=warn
 *   service:api  service:api*  trace_id:<hex>  span_id:<hex>
 *   attr.http.status:500  res.k8s.pod.name:web-1  attr.user.id:*
 *   -severity:debug  NOT x  a OR b  (a OR b) c
 */

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { TimestampSchema, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  ObservabilityService,
  SortOrder,
  type PanelSchema,
  type LogRecord as WireLogRecord,
  type Panel as WirePanel,
  type PanelPredicate as WirePredicate,
  type Span as WireSpan,
  type TimePoint,
  type TraceSummary as WireTraceSummary,
} from "@/gen/controlplane/services/observability/v1/observability_pb";

import { getControlPlaneClient } from "../client";
import type {
  DashboardSpec,
  EnvScope,
  Flamegraph,
  GetFlamegraphRequest,
  ListTracesRequest,
  ListTracesResult,
  LogRecord,
  Panel,
  PanelResult,
  Predicate,
  QueryPanelRequest,
  SearchLogsRequest,
  SearchLogsResult,
  ServiceHealth,
  ServiceOverview,
  Severity,
  Span,
  SpanStatus,
  TimeWindow,
  Trace,
  TraceSummary,
  Unit,
} from "./types";

function client() {
  return getControlPlaneClient(ObservabilityService);
}

// ── Scope and time ───────────────────────────────────────────────────────────

function timestamp(ms: number): Timestamp {
  const seconds = Math.floor(ms / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

/** Epoch ms with the sub-millisecond part kept (a span can be 40µs long). */
export function msOf(ts: Timestamp | undefined): number {
  if (!ts) return 0;
  return Number(ts.seconds) * 1000 + ts.nanos / 1e6;
}

function scope(env: EnvScope, window: TimeWindow, services: string[] = []) {
  return {
    orgId: env.orgId,
    envId: env.environmentId,
    services,
    timeRange: { start: timestamp(window.startMs), end: timestamp(window.endMs) },
  };
}

// ── Logs ─────────────────────────────────────────────────────────────────────

/** OTel SeverityNumber bands (1–4 TRACE … 21–24 FATAL), falling back to the text for producers that set none. */
export function severityOf(number: number, text: string): Severity {
  if (number >= 21) return "fatal";
  if (number >= 17) return "error";
  if (number >= 13) return "warn";
  if (number >= 9) return "info";
  if (number >= 5) return "debug";
  if (number >= 1) return "trace";
  const t = text.trim().toLowerCase();
  if (t.startsWith("fatal") || t === "panic" || t === "critical" || t === "crit") return "fatal";
  if (t.startsWith("err") || t === "e") return "error";
  if (t.startsWith("warn") || t === "w") return "warn";
  if (t.startsWith("info") || t === "i" || t === "notice") return "info";
  if (t.startsWith("debug") || t === "d") return "debug";
  if (t.startsWith("trace")) return "trace";
  return "unknown";
}

const HEX = /^[0-9a-fA-F]{1,64}$/;

/**
 * The query the server runs: what the reader typed, plus the trace/span the
 * view is scoped to. The ids are appended only when they are hex — they come
 * from the URL, and nothing else may ride into the query string that way.
 */
export function composeLogQuery(query: string, traceId?: string, spanId?: string): string {
  const terms = [query.trim()];
  if (traceId && HEX.test(traceId)) terms.push(`trace_id:${traceId}`);
  if (spanId && HEX.test(spanId)) terms.push(`span_id:${spanId}`);
  const composed = terms.filter(Boolean);
  // A reader's own OR must not swallow the scope: (a OR b) trace_id:x.
  if (composed.length > 1 && /\bOR\b/.test(composed[0])) composed[0] = `(${composed[0]})`;
  return composed.join(" ");
}

function mapLog(record: WireLogRecord, key: string): LogRecord {
  return {
    id: key,
    timeMs: msOf(record.timestamp),
    severity: severityOf(record.severityNumber, record.severityText),
    severityText: record.severityText,
    service: record.service,
    body: record.body,
    traceId: record.traceId,
    spanId: record.spanId,
    attributes: { ...record.attributes },
    resource: { ...record.resourceAttributes },
    source: record.source,
    operatorData: record.operatorData,
  };
}

export async function searchLogs(req: SearchLogsRequest): Promise<SearchLogsResult> {
  const res = await client().searchLogs({
    scope: scope(req, req.window, req.services),
    query: composeLogQuery(req.query, req.traceId, req.spanId),
    severities: req.severities.filter((s) => s !== "unknown").map((s) => s.toUpperCase()),
    order: SortOrder.NEWEST_FIRST,
    pageSize: req.limit,
    pageToken: req.cursor ?? "",
  });
  // The cursor is the page's identity; a record's index within it makes the key.
  const page = req.cursor ?? "first";
  return {
    records: res.logs.map((record, index) => mapLog(record, `${page}:${index}`)),
    nextCursor: res.nextPageToken,
  };
}

// ── Traces ───────────────────────────────────────────────────────────────────

function mapTraceSummary(t: WireTraceSummary): TraceSummary {
  return {
    traceId: t.traceId,
    rootService: t.rootService,
    rootOperation: t.rootSpanName,
    startMs: msOf(t.startTime),
    durationMs: t.durationMs,
    rootDurationMs: t.rootDurationMs,
    spanCount: t.spanCount,
    errorCount: t.errorCount,
    services: [...t.services],
    source: t.source,
    operatorData: t.operatorData,
  };
}

export async function listTraces(req: ListTracesRequest): Promise<ListTracesResult> {
  const res = await client().listTraces({
    scope: scope(req, req.window, req.services),
    errorsOnly: req.errorsOnly || undefined,
    minDurationMs: req.minDurationMs > 0 ? req.minDurationMs : undefined,
    rootSpanName: req.rootSpanName || undefined,
    pageSize: req.limit,
    pageToken: req.cursor ?? "",
  });
  const red = res.red;
  return {
    traces: res.traces.map(mapTraceSummary),
    nextCursor: res.nextPageToken,
    red: red
      ? {
          traceCount: Number(red.traceCount),
          errorTraceCount: Number(red.errorTraceCount),
          ratePerSecond: red.ratePerSecond,
          errorRate: red.errorRate,
          p50Ms: red.p50Ms,
          p95Ms: red.p95Ms,
          p99Ms: red.p99Ms,
        }
      : null,
  };
}

/** OTel status codes arrive as "Error" / "STATUS_CODE_ERROR" / "Ok" depending on the exporter. */
export function statusOf(code: string): SpanStatus {
  const c = code.toLowerCase();
  if (c.includes("error")) return "error";
  if (c.endsWith("ok")) return "ok";
  return "unset";
}

function mapSpan(s: WireSpan): Span {
  return {
    spanId: s.spanId,
    parentSpanId: s.parentSpanId,
    name: s.name,
    service: s.service,
    kind: s.kind.toLowerCase().replace(/^span_kind_/, ""),
    startMs: msOf(s.startTime),
    durationMs: Number(s.durationNs) / 1e6,
    status: statusOf(s.statusCode),
    statusMessage: s.statusMessage,
    attributes: { ...s.attributes },
    resource: { ...s.resourceAttributes },
    events: s.events.map((e) => ({ timeMs: msOf(e.time), name: e.name, attributes: { ...e.attributes } })),
    source: s.source,
    operatorData: s.operatorData,
  };
}

/** Traces are kept 7 days, so a lookup by id searches all of them. */
export async function getTrace(req: EnvScope & { traceId: string; window: TimeWindow }): Promise<Trace> {
  const res = await client().getTrace({ scope: scope(req, req.window), traceId: req.traceId });
  return { traceId: res.traceId || req.traceId, spans: res.spans.map(mapSpan), truncated: res.truncated };
}

// ── Panels ───────────────────────────────────────────────────────────────────

function pointsOf(points: TimePoint[]): { timestamps: number[]; values: (number | null)[] } {
  return {
    timestamps: points.map((p) => Math.round(msOf(p.time) / 1000)),
    values: points.map((p) => (Number.isFinite(p.value) ? p.value : null)),
  };
}

function panelInit(p: Panel): MessageInitShape<typeof PanelSchema> {
  return {
    id: p.id,
    title: p.title,
    description: p.description ?? "",
    kind: p.kind,
    signal: p.signal,
    series: p.series.map((s) => ({
      label: s.label,
      agg: s.agg,
      quantile: s.quantile ?? 0,
      field: s.field ?? "",
      metricType: s.metricType ?? "",
      where: (s.where ?? []).map(predicateInit),
    })),
    ratio: !!p.ratio,
    where: (p.where ?? []).map(predicateInit),
    groupBy: p.groupBy ?? [],
    columns: p.columns ?? [],
    unit: p.unit ?? "",
    width: p.width ?? 0,
    height: p.height ?? 0,
  };
}

function predicateInit(p: Predicate) {
  return { field: p.field, op: p.op, value: p.value ?? "", values: p.values ?? [] };
}

/** Runs one dashboard panel server-side (any line, number or table panel; search panels are not run here). */
export async function queryPanel(req: QueryPanelRequest): Promise<PanelResult> {
  const res = await client().queryPanel({
    scope: scope(req, req.window, req.services),
    panel: panelInit(req.panel),
    stepSeconds: req.stepSeconds,
  });
  return {
    kind: res.kind,
    unit: res.unit,
    stepSeconds: res.stepSeconds,
    truncated: res.truncated,
    columns: [...res.columns],
    rows: res.rows.map((r) => ({ group: { ...r.group }, values: r.values.map((v) => (Number.isFinite(v) ? v : null)) })),
    series: res.series.map((s) => ({
      label: s.label,
      group: { ...s.group },
      ...pointsOf(s.points),
      value: Number.isFinite(s.value) ? s.value : null,
    })),
  };
}

// ── Service overview ─────────────────────────────────────────────────────────

export async function getServiceOverview(req: EnvScope & { window: TimeWindow; services?: string[] }): Promise<ServiceOverview> {
  const res = await client().getServiceOverview({ scope: scope(req, req.window, req.services) });
  const step = res.trendStepSeconds > 0 ? res.trendStepSeconds : 60;
  const services: ServiceHealth[] = res.services.map((s) => {
    const requests = pointsOf(s.requestTrend);
    const errorsByTime = new Map(pointsOf(s.errorTrend).timestamps.map((t, i) => [t, s.errorTrend[i]?.value ?? 0]));
    const errorTrend = requests.timestamps.map((t) => errorsByTime.get(t) ?? 0);
    return {
      name: s.service,
      requestCount: Number(s.requestCount),
      requestRate: s.ratePerSecond,
      errorCount: Number(s.errorCount),
      errorRate: s.errorRate,
      p50Ms: s.p50Ms,
      p95Ms: s.p95Ms,
      p99Ms: s.p99Ms,
      errorLogCount: Number(s.errorLogCount),
      timestamps: requests.timestamps,
      requestTrend: requests.values,
      errorTrend,
      rateSeries: requests.values.map((v) => (v === null ? null : v / step)),
      errorRateSeries: requests.values.map((v, i) => (v ? (errorTrend[i] ?? 0) / v : null)),
      source: s.source,
      operatorData: s.operatorData,
    };
  });
  return { services, trendStepSeconds: step };
}

// ── Profiles ─────────────────────────────────────────────────────────────────

export async function getFlamegraph(req: GetFlamegraphRequest): Promise<Flamegraph> {
  const res = await client().getFlamegraph({
    scope: scope(req, req.window),
    service: req.service,
    profileType: req.profileType,
    traceId: req.traceId ?? "",
  });
  return {
    speedscopeJson: res.speedscopeJson,
    totalValue: Number(res.totalValue),
    unit: res.unit,
    stackCount: res.stackCount,
    availableProfileTypes: [...res.availableProfileTypes],
    truncated: res.truncated,
  };
}

// ── Dashboards ───────────────────────────────────────────────────────────────

function mapPredicate(p: WirePredicate): Predicate {
  return { field: p.field, op: p.op, value: p.value || undefined, values: p.values.length ? [...p.values] : undefined };
}

const UNITS: readonly Unit[] = ["", "count", "ms", "bytes", "percent"];

function mapPanel(p: WirePanel): Panel {
  return {
    id: p.id,
    title: p.title,
    description: p.description || undefined,
    kind: p.kind,
    signal: p.signal,
    series: p.series.map((s) => ({
      label: s.label,
      agg: s.agg,
      quantile: s.quantile || undefined,
      field: s.field || undefined,
      metricType: s.metricType || undefined,
      where: s.where.map(mapPredicate),
    })),
    ratio: p.ratio,
    where: p.where.map(mapPredicate),
    groupBy: [...p.groupBy],
    columns: [...p.columns],
    unit: (UNITS as readonly string[]).includes(p.unit) ? (p.unit as Unit) : "",
    width: p.width,
    height: p.height,
  };
}

export async function getDashboards(req: EnvScope & { window: TimeWindow }): Promise<DashboardSpec> {
  const res = await client().getDashboards({ scope: scope(req, req.window) });
  const spec = res.spec;
  return {
    version: spec?.version ?? "",
    project: spec?.project ?? "",
    source: res.source,
    dashboards: (spec?.dashboards ?? []).map((d) => ({
      id: d.id,
      title: d.title,
      description: d.description || undefined,
      filters: d.filters.map((f) => ({ id: f.id, label: f.label, field: f.field })),
      panels: d.panels.map(mapPanel),
    })),
  };
}
