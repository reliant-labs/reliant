// Copyright (c) 2025 Reliant Labs

/**
 * The observability RPCs — `controlplane.v1.ObservabilityService` on
 * control-plane's public listener — mapped into the console's model
 * (./types.ts). This is the only file that knows the wire shape.
 *
 * Every request carries a QueryScope (contract K1): the caller's current org
 * (verified server-side against membership), the forge project when known, the
 * environments in view as `envIds` (empty = all the caller can see; each id is
 * validated server-side as one of the org's), the services filter, the
 * attribute filters, and a time range the server caps per RPC (7 days for
 * logs, traces, the overview and workloads; 30 for metrics, profiles and
 * dashboards). The browser sends typed requests and a query STRING; it never
 * sends SQL. Attribute filters go as key/op/value DATA over an allowlist the
 * server enforces — never spliced into the log query string.
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
  ObservabilityQuotaState,
  AttributeOp as WireAttributeOp,
  MetricType as WireMetricType,
  ObservabilityService,
  Signal as WireSignal,
  SortOrder,
  WorkloadCause as WireWorkloadCause,
  WorkloadHealth as WireWorkloadHealth,
  WorkloadJobResult as WireWorkloadJobResult,
  WorkloadMetric as WireWorkloadMetric,
  WorkloadPodState as WireWorkloadPodState,
  WorkloadReleaseBasis as WireWorkloadReleaseBasis,
  type ResourceUsage as WireResourceUsage,
  type Workload as WireWorkload,
  type WorkloadEvent as WireWorkloadEvent,
  type WorkloadJob as WireWorkloadJob,
  type WorkloadPod as WireWorkloadPod,
  type PanelSchema,
  type PanelSeriesMeta as WireSeriesMeta,
  type LogRecord as WireLogRecord,
  type Span as WireSpan,
  type TimePoint,
  type TraceRedSummary as WireRed,
  type TraceSummary as WireTraceSummary,
} from "@/gen/controlplane/services/observability/v1/observability_pb";
import { WorkloadKind as WireWorkloadKind, type DeclaredWorkload as WireDeclaredWorkload } from "@/gen/controlplane/controlplane/v1/deploy_pb";

import { obsClient } from "./obsClient";
import type {
  AttributeFilter,
  AttributeOp,
  DeclaredWorkload,
  Workload,
  WorkloadCause,
  WorkloadHealth,
  WorkloadJob,
  WorkloadJobResult,
  WorkloadKind,
  WorkloadPod,
  WorkloadReleaseBasis,
  GetWorkloadRequest,
  ListAttributeValuesRequest,
  ListAttributeValuesResult,
  ListWorkloadsResult,
  ObsScope,
  ResourceUsage,
  WorkloadDetail,
  WorkloadEvent,
  WorkloadMetric,
  Flamegraph,
  GetFlamegraphRequest,
  GetServiceSignalsRequest,
  ServiceSignal,
  ServiceSignalsResult,
  ListTracesRequest,
  ListTracesResult,
  LogRecord,
  ObservabilityUsage,
  QuotaState,
  UsageSignal,
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
  TraceRed,
  TraceSummary,
} from "./types";
import { SERVICE_SIGNALS } from "./types";
import type { MetricKind, SeriesMeta } from "./metricsTypes";

function client() {
  return obsClient(ObservabilityService);
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

const WIRE_OP: Record<AttributeOp, WireAttributeOp> = {
  eq: WireAttributeOp.EQ,
  neq: WireAttributeOp.NEQ,
  exists: WireAttributeOp.EXISTS,
  in: WireAttributeOp.IN,
  contains: WireAttributeOp.CONTAINS,
};

/** The console's filters as QueryScope.filters. Values are always sent as data; the server binds them. */
export function wireFilters(filters: AttributeFilter[]) {
  return filters.map((f) => ({
    key: f.key,
    op: WIRE_OP[f.op],
    value: f.op === "in" || f.op === "exists" ? "" : (f.value ?? ""),
    values: f.op === "in" ? (f.values ?? []) : [],
  }));
}

/**
 * QueryScope. `projectId` is sent only when the console knows the project —
 * absent means "no project narrowing", while an EMPTY string would mean "the
 * project-less environments only", which no view asks for.
 */
export function wireScope(s: ObsScope, window: TimeWindow, services: string[] = []) {
  return {
    orgId: s.orgId,
    projectId: s.projectId || undefined,
    envIds: s.envIds,
    services,
    filters: wireFilters(s.filters),
    timeRange: { start: timestamp(window.startMs), end: timestamp(window.endMs) },
  };
}

const scope = wireScope;

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

export async function searchLogs(req: SearchLogsRequest, options: { signal?: AbortSignal } = {}): Promise<SearchLogsResult> {
  const res = await client().searchLogs({
    scope: scope(req, req.window, req.services),
    query: composeLogQuery(req.query, req.traceId, req.spanId),
    severities: req.severities.filter((s) => s !== "unknown").map((s) => s.toUpperCase()),
    order: SortOrder.NEWEST_FIRST,
    pageSize: req.limit,
    pageToken: req.cursor ?? "",
  }, options);
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

export async function listTraces(req: ListTracesRequest, options: { signal?: AbortSignal } = {}): Promise<ListTracesResult> {
  const res = await client().listTraces({
    scope: scope(req, req.window, req.services),
    errorsOnly: req.errorsOnly || undefined,
    minDurationMs: req.minDurationMs > 0 ? req.minDurationMs : undefined,
    rootSpanName: req.rootSpanName || undefined,
    pageSize: req.limit,
    pageToken: req.cursor ?? "",
    groupBy: req.groupBy ?? [],
  }, options);
  return {
    traces: res.traces.map(mapTraceSummary),
    nextCursor: res.nextPageToken,
    red: res.red ? mapRed(res.red) : null,
    redGroups: res.redGroups.filter((g) => g.red).map((g) => ({ group: { ...g.group }, red: mapRed(g.red!) })),
  };
}

function mapRed(red: WireRed): TraceRed {
  return {
    traceCount: Number(red.traceCount),
    errorTraceCount: Number(red.errorTraceCount),
    ratePerSecond: red.ratePerSecond,
    errorRate: red.errorRate,
    p50Ms: red.p50Ms,
    p95Ms: red.p95Ms,
    p99Ms: red.p99Ms,
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
    links: s.links.map((link) => ({ traceId: link.traceId, spanId: link.spanId })),
    source: s.source,
    operatorData: s.operatorData,
  };
}

/** Traces are kept 7 days, so a lookup by id searches all of them. */
export async function getTrace(req: ObsScope & { traceId: string; window: TimeWindow }, options: { signal?: AbortSignal } = {}): Promise<Trace> {
  const res = await client().getTrace({ scope: scope(req, req.window), traceId: req.traceId }, options);
  return {
    traceId: res.traceId || req.traceId,
    spans: res.spans.map(mapSpan),
    truncated: res.truncated,
    relatedTraces: res.relatedTraces.map((ref) => ({ traceId: ref.traceId, spanId: ref.spanId, linkedSpanId: ref.linkedSpanId })),
    relatedTracesTruncated: res.relatedTracesTruncated,
  };
}

const KIND_OF: Partial<Record<WireMetricType, MetricKind>> = {
  [WireMetricType.GAUGE]: "gauge",
  [WireMetricType.SUM]: "sum",
  [WireMetricType.HISTOGRAM]: "histogram",
  [WireMetricType.EXPONENTIAL_HISTOGRAM]: "exp_histogram",
  [WireMetricType.SUMMARY]: "summary",
};

/** A wire metric kind as the console's; one this build does not know (a newer server) is "unknown". */
export function kindOfWire(type: WireMetricType): MetricKind {
  return KIND_OF[type] ?? "unknown";
}

function mapSeriesMeta(m: WireSeriesMeta): SeriesMeta {
  return {
    label: m.label,
    metric: m.metric,
    kind: kindOfWire(m.metricType),
    agg: m.aggregation,
    quantile: m.quantile > 0 ? m.quantile : undefined,
    valueUnit: m.valueUnit,
  };
}

// ── Panels ───────────────────────────────────────────────────────────────────

function pointsOf(points: TimePoint[]): { timestamps: number[]; values: (number | null)[]; partial?: boolean[] } {
  const out: { timestamps: number[]; values: (number | null)[]; partial?: boolean[] } = {
    timestamps: points.map((p) => Math.round(msOf(p.time) / 1000)),
    // `null` is the server's gap (a formula over a missing operand): the value beside it is 0 and must be ignored.
    values: points.map((p) => (p.null || !Number.isFinite(p.value) ? null : p.value)),
  };
  if (points.some((p) => p.partial)) out.partial = points.map((p) => p.partial);
  return out;
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
    formula: p.formula ?? "",
    limit: p.limit ?? 0,
    sql: p.sql ?? "",
    funnel: p.funnel ? { steps: p.funnel.steps, window: p.funnel.window ?? "", by: p.funnel.by ?? "" } : undefined,
    hasError: !!p.hasError,
    unit: p.unit ?? "",
    width: p.width ?? 0,
    height: p.height ?? 0,
  };
}

function predicateInit(p: Predicate) {
  return { field: p.field, op: p.op, value: p.value ?? "", values: p.values ?? [] };
}

/**
 * Runs one dashboard panel server-side: line, number and table panels over a
 * signal, and the browser kinds (funnel, issues, sessions) over the tenant's
 * browser tables. Search panels are not run here.
 */
export async function queryPanel(req: QueryPanelRequest, options: { signal?: AbortSignal } = {}): Promise<PanelResult> {
  const res = await client().queryPanel(
    {
      scope: scope(req, req.window, req.services),
      panel: panelInit(req.panel),
      stepSeconds: req.stepSeconds,
      groupBy: req.groupBy ?? [],
      explain: req.explain ?? false,
    },
    options.signal ? { signal: options.signal } : undefined
  );
  return {
    kind: res.kind,
    unit: res.unit,
    stepSeconds: res.stepSeconds,
    window: req.window,
    bucketSeconds: res.bucketSeconds,
    bucketTimezone: res.bucketTimezone || undefined,
    truncated: res.truncated,
    columns: [...res.columns],
    rows: res.rows.map((r) => {
      const missing = new Set(r.missingValueIndexes);
      return { group: { ...r.group }, values: r.values.map((v, index) => (!missing.has(index) && Number.isFinite(v) ? v : null)) };
    }),
    series: res.series.map((s) => ({
      label: s.label,
      group: { ...s.group },
      ...pointsOf(s.points),
      value: Number.isFinite(s.value) ? s.value : null,
    })),
    funnelSteps: res.funnelSteps.map((step) => ({
      event: step.event,
      count: Number(step.count),
      rateFromFirst: step.rateFromFirst,
      rateFromPrevious: step.rateFromPrevious,
    })),
    issues: res.issues.map((issue) => ({
      serviceName: issue.serviceName,
      stackHash: issue.stackHash,
      exceptionType: issue.exceptionType,
      message: issue.message,
      urlPath: issue.urlPath,
      serviceVersion: issue.serviceVersion,
      events: Number(issue.events),
      sessions: Number(issue.sessions),
      firstSeenMs: msOf(issue.firstSeen),
      lastSeenMs: msOf(issue.lastSeen),
      sampleTraceId: issue.sampleTraceId,
      sampleSessionId: issue.sampleSessionId,
    })),
    sessions: res.sessions.map((session) => ({
      sessionId: session.sessionId,
      serviceName: session.serviceName,
      serviceVersion: session.serviceVersion,
      envId: session.envId,
      userId: session.userId,
      entryPath: session.entryPath,
      startMs: msOf(session.start),
      endMs: msOf(session.end),
      pageViews: Number(session.pageViews),
      interactions: Number(session.interactions),
      errors: Number(session.errors),
      hasReplay: session.hasReplay,
    })),
    seriesMeta: (res.seriesMeta ?? []).map(mapSeriesMeta),
    seriesTotal: res.seriesTotal ?? 0,
    warnings: [...(res.warnings ?? [])],
    compiledSql: (res.compiledSql ?? []).map((st) => ({ sql: st.sql, params: { ...st.params } })),
  };
}

// ── Service overview ─────────────────────────────────────────────────────────

export async function getServiceOverview(
  req: ObsScope & { window: TimeWindow; services?: string[]; groupBy?: string[] },
  options: { signal?: AbortSignal } = {}
): Promise<ServiceOverview> {
  const res = await client().getServiceOverview({ scope: scope(req, req.window, req.services), groupBy: req.groupBy ?? [] }, options);
  const step = res.trendStepSeconds > 0 ? res.trendStepSeconds : 60;
  const services: ServiceHealth[] = res.services.map((s) => {
    const requests = pointsOf(s.requestTrend);
    const errorsByTime = new Map(pointsOf(s.errorTrend).timestamps.map((t, i) => [t, s.errorTrend[i]?.value ?? 0]));
    const errorTrend = requests.timestamps.map((t) => errorsByTime.get(t) ?? 0);
    return {
      name: s.service,
      group: { ...s.group },
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

export async function getFlamegraph(req: GetFlamegraphRequest, options: { signal?: AbortSignal } = {}): Promise<Flamegraph> {
  const res = await client().getFlamegraph({
    scope: scope(req, req.window),
    service: req.service,
    profileType: req.profileType,
    traceId: req.traceId ?? "",
  }, options);
  return {
    speedscopeJson: res.speedscopeJson,
    totalValue: Number(res.totalValue),
    unit: res.unit,
    stackCount: res.stackCount,
    availableProfileTypes: [...res.availableProfileTypes],
    truncated: res.truncated,
  };
}

// ── Service signals ──────────────────────────────────────────────────────────

const signalsOf = (values: string[]): ServiceSignal[] =>
  values.filter((value): value is ServiceSignal => (SERVICE_SIGNALS as readonly string[]).includes(value));

/**
 * Which signals each service in the scope sends and declares: one bounded
 * server-side statement, cached a minute. Attribute filters are not applied
 * (a filter that matches nothing must not hide the view that would show it).
 */
export async function getServiceSignals(req: GetServiceSignalsRequest, options: { signal?: AbortSignal } = {}): Promise<ServiceSignalsResult> {
  const res = await client().getServiceSignals({ scope: scope(req, req.window) }, options);
  return {
    services: res.services.map((s) => ({
      service: s.service,
      emitting: signalsOf(s.emitting),
      declared: signalsOf(s.declared),
      declarationKnown: s.declarationKnown,
    })),
    truncated: res.truncated,
  };
}

// ── Usage and billing ────────────────────────────────────────────────────────

function quotaState(state: ObservabilityQuotaState): QuotaState {
  switch (state) {
    case ObservabilityQuotaState.OK:
      return "ok";
    case ObservabilityQuotaState.OVER_100:
      return "over_100";
    case ObservabilityQuotaState.OVER_150:
      return "over_150";
    default:
      return "unknown";
  }
}

const USAGE_SIGNALS: readonly UsageSignal[] = ["logs", "traces", "metrics", "profiles"];

/** The org's month-to-date usage. Org-scoped (not environment-scoped): the allowance is per org. */
export async function getObservabilityUsage(orgId: string): Promise<ObservabilityUsage> {
  const res = await client().getObservabilityUsage({ orgId });
  const signal = (value: string): UsageSignal =>
    (USAGE_SIGNALS as readonly string[]).includes(value) ? (value as UsageSignal) : "logs";
  return {
    periodStartMs: msOf(res.periodStart),
    periodEndMs: msOf(res.periodEnd),
    measuredThroughMs: res.measuredThrough ? msOf(res.measuredThrough) : null,
    usedBytes: Number(res.usedBytes),
    allowanceBytes: Number(res.allowanceBytes),
    overageCents: Number(res.overageCents),
    projectedBytes: Number(res.projectedBytes),
    projectedOverageCents: Number(res.projectedOverageCents),
    rateCentsPerGb: res.rateCentsPerGb,
    state: quotaState(res.state),
    usageState: quotaState(res.usageState),
    funded: res.funded,
    exempt: res.exempt,
    spendCapCents: Number(res.spendCapCents),
    spendCapReached: res.spendCapReached,
    browserBytes: Number(res.browserBytes),
    browserBilledBytes: Number(res.browserBilledBytes),
    browserCapBytes: Number(res.browserCapBytes),
    metadataBytes: Number(res.metadataBytes),
    bySignal: res.bySignal.map((s) => ({
      signal: signal(s.signal),
      billedBytes: Number(s.billedBytes),
      storedBytes: Number(s.storedBytes),
      rows: Number(s.rows),
    })),
    byService: res.byService.map((s) => ({ service: s.service, signal: signal(s.signal), billedBytes: Number(s.billedBytes) })),
  };
}

// ── Attribute values ─────────────────────────────────────────────────────────

const WIRE_SIGNAL = { traces: WireSignal.TRACES, logs: WireSignal.LOGS, metrics: WireSignal.METRICS } as const;

/**
 * The values an allowlisted key takes in the scope, most frequent first — the
 * filter bar's autocomplete. The server ignores filters on the SAME key, so
 * choosing a second value is never narrowed to the first one.
 */
export async function listAttributeValues(req: ListAttributeValuesRequest, options: { signal?: AbortSignal } = {}): Promise<ListAttributeValuesResult> {
  const res = await client().listAttributeValues({
    scope: scope(req, req.window),
    key: req.key,
    prefix: req.prefix || undefined,
    signal: req.signal ? WIRE_SIGNAL[req.signal] : undefined,
    limit: req.limit ?? 50,
  }, options);
  return { values: res.values.map((v) => ({ value: v.value, count: Number(v.count) })), truncated: res.truncated };
}

// ── Workloads (customer metadata) ────────────────────────────────────────────

function mapUsage(u: WireResourceUsage | undefined): ResourceUsage | null {
  if (!u) return null;
  const opt = (v: number | undefined) => (v === undefined || !Number.isFinite(v) ? null : v);
  return {
    latest: u.latest,
    p95: u.p95,
    limitUtilizationLatest: opt(u.limitUtilizationLatest),
    limitUtilizationP95: opt(u.limitUtilizationP95),
    requestUtilizationLatest: opt(u.requestUtilizationLatest),
    requestUtilizationP95: opt(u.requestUtilizationP95),
    limit: opt(u.limit),
    request: opt(u.request),
  };
}

function mapEvent(e: WireWorkloadEvent): WorkloadEvent {
  return {
    timeMs: msOf(e.time),
    envId: e.envId,
    type: e.type,
    reason: e.reason,
    message: e.message,
    count: Number(e.count),
    objectKind: e.objectKind,
    objectName: e.objectName,
    container: e.container,
    plainReason: e.plainReason,
  };
}

const WORKLOAD_KIND: Record<WireWorkloadKind, WorkloadKind> = {
  [WireWorkloadKind.UNSPECIFIED]: "unknown",
  [WireWorkloadKind.DEPLOYMENT]: "deployment",
  [WireWorkloadKind.STATEFUL_SET]: "statefulset",
  [WireWorkloadKind.DAEMON_SET]: "daemonset",
  [WireWorkloadKind.CRON_JOB]: "cronjob",
  [WireWorkloadKind.JOB]: "job",
};

const WORKLOAD_HEALTH: Record<WireWorkloadHealth, WorkloadHealth> = {
  [WireWorkloadHealth.UNSPECIFIED]: "unknown",
  [WireWorkloadHealth.HEALTHY]: "healthy",
  [WireWorkloadHealth.DEGRADED]: "degraded",
  [WireWorkloadHealth.CRASH_LOOPING]: "crash_looping",
  [WireWorkloadHealth.PENDING]: "pending",
  [WireWorkloadHealth.SUSPENDED]: "suspended",
  [WireWorkloadHealth.UNKNOWN]: "unknown",
};

const WORKLOAD_CAUSE: Record<WireWorkloadCause, WorkloadCause> = {
  [WireWorkloadCause.UNSPECIFIED]: "none",
  [WireWorkloadCause.NONE]: "none",
  [WireWorkloadCause.WORKLOAD]: "workload",
  [WireWorkloadCause.PLATFORM]: "platform",
  [WireWorkloadCause.UNDETERMINED]: "undetermined",
};

const RELEASE_BASIS: Record<WireWorkloadReleaseBasis, WorkloadReleaseBasis> = {
  [WireWorkloadReleaseBasis.UNSPECIFIED]: "none",
  [WireWorkloadReleaseBasis.DIGEST]: "digest",
  [WireWorkloadReleaseBasis.PROMOTION]: "promotion",
};

const JOB_RESULT: Record<WireWorkloadJobResult, WorkloadJobResult> = {
  [WireWorkloadJobResult.UNSPECIFIED]: "unknown",
  [WireWorkloadJobResult.RUNNING]: "running",
  [WireWorkloadJobResult.SUCCEEDED]: "succeeded",
  [WireWorkloadJobResult.FAILED]: "failed",
};

// A value this build does not know (a newer server) reads as unknown, never as undefined.
const kindOf = (k: WireWorkloadKind): WorkloadKind => WORKLOAD_KIND[k] ?? "unknown";
const healthOf = (h: WireWorkloadHealth): WorkloadHealth => WORKLOAD_HEALTH[h] ?? "unknown";
const msOrNull = (ts: Timestamp | undefined): number | null => (ts ? msOf(ts) : null);

function mapPod(p: WireWorkloadPod): WorkloadPod {
  return {
    name: p.name,
    uid: p.uid,
    state: p.state === WireWorkloadPodState.RUNNING ? "running" : p.state === WireWorkloadPodState.GONE ? "gone" : "unknown",
    restarts: Number(p.restarts),
    lastExitReason: p.lastExitReason,
    lastExitAtMs: msOrNull(p.lastExitAt),
    node: p.node,
    firstSeenMs: msOrNull(p.firstSeen),
    lastSeenMs: msOrNull(p.lastSeen),
    ageIsLowerBound: p.ageIsLowerBound,
    latestWarning: p.latestWarning ? mapEvent(p.latestWarning) : null,
  };
}

function mapDeclared(d: WireDeclaredWorkload): DeclaredWorkload {
  return {
    name: d.name,
    kind: kindOf(d.kind),
    specRecorded: d.specRecorded,
    replicas: d.replicas ?? null,
    cpuRequestMillicores: Number(d.cpuRequestMillicores),
    cpuLimitMillicores: Number(d.cpuLimitMillicores),
    memoryRequestBytes: Number(d.memoryRequestBytes),
    memoryLimitBytes: Number(d.memoryLimitBytes),
    ports: d.ports.map((p) => ({ name: p.name, port: p.port, protocol: p.protocol })),
    envNames: [...d.envNames],
    image: d.image,
    sourcePath: d.sourcePath,
    sourceRepo: d.sourceRepo,
    sourceCommit: d.sourceCommit,
    bundleId: d.bundleId,
  };
}

function mapWorkload(w: WireWorkload): Workload {
  return {
    envId: w.envId,
    envName: w.envName,
    workload: w.workload,
    namespace: w.namespace,
    serviceVersion: w.serviceVersion,
    replicasObserved: w.replicasObserved,
    restartsInWindow: Number(w.restartsInWindow),
    latestWarning: w.latestWarning ? mapEvent(w.latestWarning) : null,
    containers: w.containers.map((c) => ({
      container: c.container,
      imageName: c.imageName,
      imageTag: c.imageTag,
      restartsInWindow: Number(c.restartsInWindow),
      lastTerminatedReason: c.lastTerminatedReason,
      lastTerminatedAtMs: c.lastTerminatedAt ? msOf(c.lastTerminatedAt) : null,
      cpu: mapUsage(c.cpu),
      memory: mapUsage(c.memory),
    })),
    kind: kindOf(w.kind),
    health: healthOf(w.health),
    healthReason: w.healthReason,
    healthCause: WORKLOAD_CAUSE[w.healthCause] ?? "none",
    healthDetail: w.healthDetail,
    readyReplicas: w.readyReplicas ?? null,
    desiredReplicas: w.desiredReplicas ?? null,
    releaseVersion: w.releaseVersion,
    releaseBasis: w.releaseVersion ? (RELEASE_BASIS[w.releaseBasis] ?? "none") : "none",
    pods: w.pods.map(mapPod),
    declared: w.declared ? mapDeclared(w.declared) : null,
  };
}

function mapJob(j: WireWorkloadJob): WorkloadJob {
  return {
    envId: j.envId,
    envName: j.envName,
    name: j.name,
    workload: j.workload || j.name,
    result: JOB_RESULT[j.result] ?? "unknown",
    reason: j.reason,
    releaseVersion: j.releaseVersion,
    startedAtMs: msOrNull(j.startedAt),
    finishedAtMs: msOrNull(j.finishedAt),
    durationSeconds: Number(j.durationSeconds),
  };
}

export async function listWorkloads(req: ObsScope & { window: TimeWindow; services?: string[] }, options: { signal?: AbortSignal } = {}): Promise<ListWorkloadsResult> {
  const res = await client().listWorkloads({ scope: scope(req, req.window, req.services) }, options);
  return {
    truncated: res.truncated,
    workloads: res.workloads.map(mapWorkload),
    jobs: res.jobs.map(mapJob),
    jobsTruncated: res.jobsTruncated,
  };
}

const WORKLOAD_METRIC: Partial<Record<WireWorkloadMetric, WorkloadMetric>> = {
  [WireWorkloadMetric.CPU_USAGE]: "cpu_usage",
  [WireWorkloadMetric.CPU_LIMIT_UTILIZATION]: "cpu_limit_utilization",
  [WireWorkloadMetric.CPU_REQUEST_UTILIZATION]: "cpu_request_utilization",
  [WireWorkloadMetric.MEMORY_WORKING_SET]: "memory_working_set",
  [WireWorkloadMetric.MEMORY_LIMIT_UTILIZATION]: "memory_limit_utilization",
  [WireWorkloadMetric.MEMORY_REQUEST_UTILIZATION]: "memory_request_utilization",
  [WireWorkloadMetric.RESTARTS]: "restarts",
};

export async function getWorkload(req: GetWorkloadRequest, options: { signal?: AbortSignal } = {}): Promise<WorkloadDetail> {
  const res = await client().getWorkload({
    scope: scope(req, req.window),
    workload: req.workload,
    container: req.container ?? "",
    stepSeconds: req.stepSeconds ?? 0,
  }, options);
  return {
    stepSeconds: res.stepSeconds,
    truncated: res.truncated,
    // A metric this build does not know is dropped rather than drawn unlabelled.
    series: res.series.flatMap((s) => {
      const metric = WORKLOAD_METRIC[s.metric];
      return metric ? [{ envId: s.envId, container: s.container, metric, unit: s.unit, ...pointsOf(s.points) }] : [];
    }),
    events: res.events.map(mapEvent),
    imageChanges: res.imageChanges.map((c) => ({
      envId: c.envId,
      container: c.container,
      imageName: c.imageName,
      imageTag: c.imageTag,
      firstSeenMs: msOf(c.firstSeen),
      lastSeenMs: msOf(c.lastSeen),
    })),
    versionChanges: res.versionChanges.map((c) => ({
      envId: c.envId,
      serviceVersion: c.serviceVersion,
      firstSeenMs: msOf(c.firstSeen),
      lastSeenMs: msOf(c.lastSeen),
    })),
    workloads: res.workloads.map(mapWorkload),
  };
}
