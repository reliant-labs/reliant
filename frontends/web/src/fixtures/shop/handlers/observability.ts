// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's observability handlers: the reads the console's observability views
 * make, answered on the WIRE, as control-plane would answer them. The pack
 * composes them with the other services' handlers (../index.ts shopScenario).
 *
 * The data is shop's story (shopStory.ts, answered by model/shopApi.ts),
 * encoded into the proto messages: each handler decodes the request into the
 * console's model, asks the story's model, and encodes the answer. So the demo host
 * runs the console's REAL client — api.ts, its mappers, the read scheduler —
 * over the mock transport, and swapping in the demo-data gateway
 * (Forge/Demo/demoTransport.ts, `passthrough: "first"`) is a config flip, not new code.
 *
 * Every ObservabilityService read the console makes is served here
 * (telemetry, panels, workloads, the service graph, metrics, release health,
 * each frontend's replay switch); the other services' reads are their own
 * modules beside this one. A read the client never makes falls through to
 * forge's generated default (handlers/defaults.ts).
 */

import { create } from "@bufbuild/protobuf";
import { mockService } from "@reliantlabs/forge-web-runtime/mock-transport";

import {
  AttributeOp as WireAttributeOp,
  FrontendReplaySource as WireReplaySource,
  FrontendReplayState as WireReplayState,
  GraphEdgeKind,
  GraphEvidenceKind,
  GraphNodeKind,
  MetricTemporality as WireTemporality,
  MetricType as WireMetricType,
  ReleaseFindingCode as WireFindingCode,
  ReleaseVerdict as WireVerdict,
  Signal as WireSignal,
  WorkloadCause as WireWorkloadCause,
  WorkloadHealth as WireWorkloadHealth,
  WorkloadJobResult as WireWorkloadJobResult,
  WorkloadMetric as WireWorkloadMetric,
  WorkloadPodState as WireWorkloadPodState,
  WorkloadReleaseBasis as WireWorkloadReleaseBasis,
  GetFlamegraphResponseSchema,
  GetServiceOverviewResponseSchema,
  GetServiceSignalsResponseSchema,
  GetTraceResponseSchema,
  ListTracesResponseSchema,
  ObservabilityService,
  SearchLogsResponseSchema,
  type GetFlamegraphRequest,
  type GetServiceOverviewRequest,
  type GetServiceSignalsRequest,
  type GetTraceRequest,
  type ListTracesRequest,
  type Panel as WirePanel,
  type QueryScope,
  type SearchLogsRequest,
} from "@/gen/controlplane/services/observability/v1/observability_pb";
import { DeclaredSpecGap as WireDeclaredSpecGap, WorkloadKind as WireWorkloadKind } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import type { ReleaseFindingCode, ReleaseHealth, ServiceWindowStats } from "@/services/controlPlane/observability/deliveryTypes";
import type { GraphEdge, GraphNode, GraphQuality } from "@/services/controlPlane/observability/graphTypes";
import type { MetricCatalogEntry, MetricKind } from "@/services/controlPlane/observability/metricsTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type {
  AttributeOp,
  DeclaredSpecGap,
  DeclaredWorkload,
  LogRecord,
  ObsScope,
  Panel,
  Predicate,
  ResourceUsage,
  Severity,
  Span,
  TimeWindow,
  TraceRed,
  TraceSummary,
  Workload,
  WorkloadCause,
  WorkloadEvent,
  WorkloadHealth,
  WorkloadJobResult,
  WorkloadKind,
  WorkloadMetric,
  WorkloadReleaseBasis,
} from "@/services/controlPlane/observability/types";

import forgeDashboardsRaw from "../model/forge-dashboards.json?raw";
import { createShopApi } from "../model/shopApi";
import { demoNow } from "../shopStory";
import { completed } from "./defaults";
import { alertingHandlers } from "./alerting";
import { observabilityBillingHandlers } from "./observabilityBilling";
import { dashboardsHandlers } from "./dashboards";
import { deliveryHandlers } from "./delivery";
import { issuesHandlers, wireIssue } from "./issues";
import { rumHandlers } from "./rum";
import { userHandlers } from "./user";
import { dbl, instantOf, int64, msOfDuration, points, ts, tsOpt, windowOf, type PackClock } from "./wire";

const SERVICE = ObservabilityService.typeName;
const rpc = (method: string) => `${SERVICE}/${method}`;

/** The telemetry reads, by ObservabilityApi key → RPC. */
const TELEMETRY_RPCS = {
  listTraces: rpc("ListTraces"),
  getTrace: rpc("GetTrace"),
  searchLogs: rpc("SearchLogs"),
  getServiceOverview: rpc("GetServiceOverview"),
  getFlamegraph: rpc("GetFlamegraph"),
  getServiceSignals: rpc("GetServiceSignals"),
} as const satisfies Partial<Record<keyof ObservabilityApi, string>>;

// ── Wire ↔ model ─────────────────────────────────────────────────────────────

const OP: Record<number, AttributeOp> = {
  [WireAttributeOp.EQ]: "eq",
  [WireAttributeOp.NEQ]: "neq",
  [WireAttributeOp.EXISTS]: "exists",
  [WireAttributeOp.IN]: "in",
  [WireAttributeOp.CONTAINS]: "contains",
};

/**
 * A QueryScope as the console's ObsScope, window and services. A relative
 * time range (the generated hooks' "the last hour") is resolved against `clock`,
 * and a scope with no time range reads the hour before it: the PACK's clock
 * (shopStory.ts demoNow), which the story is told at, never the wall clock,
 * which a test or a pinned pack is not at.
 */
export function modelScope(scope: QueryScope | undefined, clock: PackClock = demoNow): ObsScope & { window: TimeWindow; services: string[] } {
  const now = instantOf(clock);
  const window = windowOf(scope?.timeRange, now);
  return {
    orgId: scope?.orgId ?? "",
    projectId: scope?.projectId,
    envIds: [...(scope?.envIds ?? [])],
    ...(scope?.platformScope ? { platformScope: true } : {}),
    filters: (scope?.filters ?? []).map((f) => ({ key: f.key, op: OP[f.op] ?? "eq", value: f.value, values: [...f.values] })),
    window: window && window.endMs > window.startMs ? window : { startMs: now - 3_600_000, endMs: now },
    services: [...(scope?.services ?? [])],
  };
}

/** The window a paged read was answered over, echoed as control-plane echoes it, for the next page to ask again. */
const effectiveRange = (window: TimeWindow) => ({ start: ts(window.startMs), end: ts(window.endMs) });

/** SearchLogs' query back into what the reader typed and the trace/span it is scoped to (api.ts composeLogQuery). */
export function splitLogQuery(query: string): { query: string; traceId?: string; spanId?: string } {
  let traceId: string | undefined;
  let spanId: string | undefined;
  const rest = query
    .replace(/\btrace_id:([0-9a-fA-F]+)/g, (_, id: string) => ((traceId = id), ""))
    .replace(/\bspan_id:([0-9a-fA-F]+)/g, (_, id: string) => ((spanId = id), ""))
    .trim()
    .replace(/^\((.*)\)$/, "$1");
  return { query: rest, traceId, spanId };
}

const SEVERITY_NUMBER: Record<Severity, number> = { trace: 1, debug: 5, info: 9, warn: 13, error: 17, fatal: 21, unknown: 0 };

function wireLog(r: LogRecord) {
  return {
    timestamp: ts(r.timeMs),
    severityText: r.severityText,
    severityNumber: SEVERITY_NUMBER[r.severity] ?? 0,
    service: r.service,
    body: r.body,
    traceId: r.traceId,
    spanId: r.spanId,
    attributes: { ...r.attributes },
    resourceAttributes: { ...r.resource },
    source: r.source,
    operatorData: r.operatorData,
  };
}

function wireSummary(t: TraceSummary) {
  return {
    traceId: t.traceId,
    startTime: ts(t.startMs),
    durationMs: t.durationMs,
    rootService: t.rootService,
    rootSpanName: t.rootOperation,
    spanCount: t.spanCount,
    errorCount: t.errorCount,
    services: [...t.services],
    source: t.source,
    operatorData: t.operatorData,
    rootDurationMs: t.rootDurationMs,
    userId: t.userId,
    sessionId: t.sessionId,
    country: t.country,
    region: t.region,
    city: t.city,
    client: { ...t.client },
    frontendVersion: t.frontendVersion,
    backendBuilds: t.backendBuilds.map((b) => ({ service: b.service, version: b.version })),
  };
}

function wireRed(red: TraceRed) {
  return {
    traceCount: BigInt(red.traceCount),
    errorTraceCount: BigInt(red.errorTraceCount),
    ratePerSecond: red.ratePerSecond,
    errorRate: red.errorRate,
    p50Ms: red.p50Ms,
    p95Ms: red.p95Ms,
    p99Ms: red.p99Ms,
  };
}

const STATUS_CODE = { ok: "Ok", error: "Error", unset: "Unset" } as const;

function wireSpan(s: Span) {
  return {
    spanId: s.spanId,
    parentSpanId: s.parentSpanId,
    name: s.name,
    // api.ts lower-cases and strips SPAN_KIND_: "SPAN_KIND_SERVER" → "server".
    kind: `SPAN_KIND_${s.kind.toUpperCase()}`,
    service: s.service,
    startTime: ts(s.startMs),
    durationNs: BigInt(Math.round(s.durationMs * 1e6)),
    statusCode: STATUS_CODE[s.status as keyof typeof STATUS_CODE] ?? "Unset",
    statusMessage: s.statusMessage,
    attributes: { ...s.attributes },
    resourceAttributes: { ...s.resource },
    events: s.events.map((e) => ({ time: ts(e.timeMs), name: e.name, attributes: { ...e.attributes } })),
    links: (s.links ?? []).map((l) => ({ traceId: l.traceId, spanId: l.spanId })),
    source: s.source,
    operatorData: s.operatorData,
  };
}

// ── The handlers ─────────────────────────────────────────────────────────────

/**
 * Every read the console's observability views make (the real client's,
 * services/controlPlane/observability), over `fixtures` (a model-level
 * ObservabilityApi; the sample app by default), keyed "pkg.Service/Method":
 * ObservabilityService's here, each other service's from its own module.
 * Each returns a proto message (create()), as a server would.
 */
export function observabilityHandlers(fixtures: ObservabilityApi = createShopApi(), { clock = demoNow }: { clock?: PackClock } = {}) {
  return {
    ...observabilityServiceHandlers(fixtures, clock),
    ...alertingHandlers(fixtures, { clock }),
    ...deliveryHandlers(fixtures),
    ...issuesHandlers(fixtures, { clock }),
    ...userHandlers(fixtures),
    ...rumHandlers(fixtures),
    ...dashboardsHandlers(fixtures),
    // The demo has no billing: answered as a control plane without the billing API.
    ...observabilityBillingHandlers(),
  };
}

/** ObservabilityService's reads; a relative window resolves against the pack's `clock` (modelScope). */
function observabilityServiceHandlers(fixtures: ObservabilityApi, clock: PackClock) {
  return {
    [TELEMETRY_RPCS.listTraces]: async (req: ListTracesRequest) => {
      const scope = modelScope(req.scope, clock);
      const res = await fixtures.listTraces({
        ...scope,
        rootSpanName: req.rootSpanName ?? "",
        errorsOnly: req.errorsOnly ?? false,
        minDurationMs: req.minDurationMs ?? 0,
        userId: req.userId,
        sessionId: req.sessionId,
        browser: req.browser,
        os: req.os,
        frontendVersion: req.frontendVersion,
        backendVersion: req.backendVersion,
        limit: req.pageSize,
        cursor: req.pageToken || undefined,
        groupBy: [...req.groupBy],
      });
      return create(ListTracesResponseSchema, {
        traces: res.traces.map(wireSummary),
        nextPageToken: res.nextCursor,
        red: res.red ? wireRed(res.red) : undefined,
        redGroups: res.redGroups.map((g) => ({ group: { ...g.group }, red: wireRed(g.red) })),
        effectiveTimeRange: effectiveRange(scope.window),
      });
    },
    [TELEMETRY_RPCS.getTrace]: async (req: GetTraceRequest) => {
      const scope = modelScope(req.scope, clock);
      const trace = await fixtures.getTrace({ ...scope, traceId: req.traceId });
      return create(GetTraceResponseSchema, {
        traceId: trace.traceId,
        spans: trace.spans.map(wireSpan),
        truncated: trace.truncated,
        relatedTraces: (trace.relatedTraces ?? []).map((r) => ({ ...r })),
        relatedTracesTruncated: trace.relatedTracesTruncated ?? false,
      });
    },
    [TELEMETRY_RPCS.searchLogs]: async (req: SearchLogsRequest) => {
      const scope = modelScope(req.scope, clock);
      const { query, traceId, spanId } = splitLogQuery(req.query);
      const res = await fixtures.searchLogs({
        ...scope,
        query,
        traceId,
        spanId,
        severities: req.severities.map((s) => s.toLowerCase() as Severity),
        limit: req.pageSize,
        cursor: req.pageToken || undefined,
      });
      return create(SearchLogsResponseSchema, { logs: res.records.map(wireLog), nextPageToken: res.nextCursor, effectiveTimeRange: effectiveRange(scope.window) });
    },
    [TELEMETRY_RPCS.getServiceOverview]: async (req: GetServiceOverviewRequest) => {
      const scope = modelScope(req.scope, clock);
      const res = await fixtures.getServiceOverview({ ...scope, groupBy: [...req.groupBy] });
      return create(GetServiceOverviewResponseSchema, {
        trendStepSeconds: res.trendStepSeconds,
        services: res.services.map((s) => ({
          service: s.name,
          group: { ...s.group },
          requestCount: BigInt(Math.round(s.requestCount)),
          ratePerSecond: s.requestRate,
          errorCount: BigInt(Math.round(s.errorCount)),
          errorRate: s.errorRate,
          p50Ms: s.p50Ms,
          p95Ms: s.p95Ms,
          p99Ms: s.p99Ms,
          errorLogCount: BigInt(Math.round(s.errorLogCount)),
          requestTrend: points(s.timestamps, s.requestTrend),
          errorTrend: points(s.timestamps, s.errorTrend),
          source: s.source,
          operatorData: s.operatorData,
        })),
      });
    },
    [TELEMETRY_RPCS.getFlamegraph]: async (req: GetFlamegraphRequest) => {
      const scope = modelScope(req.scope, clock);
      const res = await fixtures.getFlamegraph({ ...scope, service: req.service, profileType: req.profileType, traceId: req.traceId || undefined });
      return create(GetFlamegraphResponseSchema, {
        speedscopeJson: res.speedscopeJson,
        totalValue: BigInt(Math.round(res.totalValue)),
        unit: res.unit,
        stackCount: res.stackCount,
        availableProfileTypes: [...res.availableProfileTypes],
        truncated: res.truncated,
      });
    },
    [TELEMETRY_RPCS.getServiceSignals]: async (req: GetServiceSignalsRequest) => {
      const res = await fixtures.getServiceSignals(modelScope(req.scope, clock));
      return create(GetServiceSignalsResponseSchema, {
        truncated: res.truncated,
        services: res.services.map((s) => ({ service: s.service, emitting: [...s.emitting], declared: [...s.declared], declarationKnown: s.declarationKnown })),
      });
    },
    ...panelHandlers(fixtures, clock),
    ...workloadHandlers(fixtures, clock),
    ...graphHandlers(fixtures, clock),
    ...metricHandlers(fixtures, clock),
    ...releaseHealthHandlers(fixtures),
  };
}

// ── Panels and attribute values ──────────────────────────────────────────────

/** A wire Panel back into the console's (api.ts panelInit, run backwards). */
function modelPanel(p: WirePanel | undefined): Panel {
  const predicate = (w: { field: string; op: string; value: string; values: string[] }): Predicate => ({ field: w.field, op: w.op, value: w.value, values: [...w.values] });
  return {
    id: p?.id ?? "",
    title: p?.title ?? "",
    description: p?.description ?? "",
    kind: p?.kind ?? "",
    signal: p?.signal ?? "",
    series: (p?.series ?? []).map((s) => ({ label: s.label, agg: s.agg, quantile: s.quantile, field: s.field, metricType: s.metricType, where: s.where.map(predicate) })),
    ratio: p?.ratio ?? false,
    where: (p?.where ?? []).map(predicate),
    groupBy: [...(p?.groupBy ?? [])],
    columns: [...(p?.columns ?? [])],
    formula: p?.formula ?? "",
    limit: p?.limit ?? 0,
    sql: p?.sql ?? "",
    funnel: p?.funnel ? { steps: [...p.funnel.steps], window: p.funnel.window, by: p.funnel.by } : undefined,
    hasError: p?.hasError ?? false,
    unit: (p?.unit || undefined) as Panel["unit"],
    width: p?.width ?? 0,
    height: p?.height ?? 0,
  };
}

/**
 * Which of shop's dashboards a sql panel is from, by its CONTENT: the panel id
 * and the statement (a figure's own, its `.trend` sparkline's or its
 * `.subtitle`'s). A server answers a sql panel from its statement alone; the
 * story answers the built-in analytics panels by `<dashboard>/<panel>`
 * (productAnalytics.ts), and the console names the dashboard only in the
 * Forge-Panel header, a label the transport does not hand a handler. An id
 * repeats across dashboards only with the same statement, which answers the
 * same, so the first match is the answer.
 */
let sqlPanelIndex: Map<string, string> | null = null;
function dashboardOfSqlPanel(panel: Panel): string | undefined {
  if (!sqlPanelIndex) {
    sqlPanelIndex = new Map();
    const index = sqlPanelIndex;
    const { dashboards } = JSON.parse(forgeDashboardsRaw) as { dashboards: { id: string; panels: unknown[] }[] };
    // Every statement-bearing figure in a spec, panels and the figures nested in one (an attention panel's).
    type Figure = { id?: unknown; sql?: unknown; trendSql?: unknown; subtitleSql?: unknown };
    const walk = (dashboard: string, value: unknown): void => {
      if (Array.isArray(value)) return value.forEach((v) => walk(dashboard, v));
      if (!value || typeof value !== "object") return;
      const f = value as Figure;
      if (typeof f.id === "string") {
        for (const [id, sql] of [[f.id, f.sql], [`${f.id}.trend`, f.trendSql], [`${f.id}.subtitle`, f.subtitleSql]] as const) {
          const key = `${id}\n${typeof sql === "string" ? sql : ""}`;
          if (typeof sql === "string" && sql && !index.has(key)) index.set(key, dashboard);
        }
      }
      Object.values(value).forEach((v) => walk(dashboard, v));
    };
    for (const d of dashboards) walk(d.id, d.panels);
  }
  return panel.kind === "sql" ? sqlPanelIndex.get(`${panel.id}\n${panel.sql ?? ""}`) : undefined;
}

const WIRE_METRIC_TYPE: Record<string, WireMetricType> = {
  gauge: WireMetricType.GAUGE,
  sum: WireMetricType.SUM,
  histogram: WireMetricType.HISTOGRAM,
  exp_histogram: WireMetricType.EXPONENTIAL_HISTOGRAM,
  summary: WireMetricType.SUMMARY,
};
const wireMetricType = (kind: string | undefined): WireMetricType => (kind ? (WIRE_METRIC_TYPE[kind] ?? WireMetricType.UNSPECIFIED) : WireMetricType.UNSPECIFIED);
const modelMetricKind = (t: WireMetricType | undefined): MetricKind | undefined =>
  t === undefined || t === WireMetricType.UNSPECIFIED ? undefined : ((Object.entries(WIRE_METRIC_TYPE).find(([, v]) => v === t)?.[0] as MetricKind | undefined) ?? undefined);

const SIGNAL_OF: Partial<Record<WireSignal, "traces" | "logs" | "metrics">> = { [WireSignal.TRACES]: "traces", [WireSignal.LOGS]: "logs", [WireSignal.METRICS]: "metrics" };

function panelHandlers(fixtures: ObservabilityApi, clock: PackClock) {
  return completed(ObservabilityService, mockService(ObservabilityService, {
    queryPanel: async (req) => {
      const scope = modelScope(req.scope, clock);
      const panel = modelPanel(req.panel);
      const res = await fixtures.queryPanel({
        ...scope,
        panel,
        stepSeconds: req.stepSeconds,
        groupBy: [...req.groupBy],
        explain: req.explain,
        dashboardId: dashboardOfSqlPanel(panel),
      });
      return {
        kind: res.kind,
        unit: res.unit,
        stepSeconds: res.stepSeconds,
        bucketSeconds: res.bucketSeconds ?? 0,
        bucketTimezone: res.bucketTimezone ?? "",
        truncated: res.truncated,
        columns: [...res.columns],
        rows: res.rows.map((r) => ({
          group: { ...r.group },
          values: r.values.map((v) => v ?? 0),
          missingValueIndexes: r.values.flatMap((v, i) => (v === null ? [i] : [])),
        })),
        series: res.series.map((s) => ({ label: s.label, group: { ...s.group }, points: points(s.timestamps, s.values, s.partial), value: dbl(s.value) })),
        funnelSteps: res.funnelSteps.map((s) => ({ event: s.event, count: int64(s.count), rateFromFirst: s.rateFromFirst, rateFromPrevious: s.rateFromPrevious })),
        issues: res.issues.map((i) => ({ ...i, events: int64(i.events), sessions: int64(i.sessions), firstSeen: ts(i.firstSeenMs), lastSeen: ts(i.lastSeenMs) })),
        sessions: res.sessions.map((s) => ({ ...s, start: ts(s.startMs), end: ts(s.endMs), pageViews: int64(s.pageViews), interactions: int64(s.interactions), errors: int64(s.errors) })),
        heatmap: (res.heatmap ?? []).map((c) => ({ time: ts(c.timeSec * 1000), low: c.low, high: c.high, count: c.count })),
        seriesMeta: res.seriesMeta.map((m) => ({ label: m.label, metric: m.metric, metricType: wireMetricType(m.kind), aggregation: m.agg, quantile: m.quantile ?? 0, valueUnit: m.valueUnit })),
        seriesTotal: res.seriesTotal,
        warnings: [...res.warnings],
        compiledSql: res.compiledSql.map((c) => ({ sql: c.sql, params: { ...c.params } })),
      };
    },
    listAttributeValues: async (req) => {
      const res = await fixtures.listAttributeValues({ ...modelScope(req.scope, clock), key: req.key, prefix: req.prefix ?? "", signal: req.signal === undefined ? undefined : SIGNAL_OF[req.signal], limit: req.limit });
      return { values: res.values.map((v) => ({ value: v.value, count: int64(v.count) })), truncated: res.truncated };
    },
    listFrontendReplay: async (req) => {
      const res = await fixtures.listFrontendReplay({ orgId: req.orgId, projectId: req.projectId, envIds: [...req.envIds] });
      return {
        frontends: res.frontends.map((f) => ({ ...f, state: WIRE_REPLAY_STATE[f.state], source: WIRE_REPLAY_SOURCE[f.source] })),
        unavailableEnvIds: [...res.unavailableEnvIds],
      };
    },
  }));
}

const WIRE_REPLAY_STATE = { on: WireReplayState.ON, off: WireReplayState.OFF, blocked: WireReplayState.BLOCKED, unknown: WireReplayState.UNKNOWN } as const;
const WIRE_REPLAY_SOURCE = { live: WireReplaySource.LIVE, release: WireReplaySource.RELEASE, none: WireReplaySource.NONE } as const;

// ── Workloads ────────────────────────────────────────────────────────────────

const WIRE_WORKLOAD_KIND: Record<WorkloadKind, WireWorkloadKind> = {
  deployment: WireWorkloadKind.DEPLOYMENT,
  statefulset: WireWorkloadKind.STATEFUL_SET,
  daemonset: WireWorkloadKind.DAEMON_SET,
  cronjob: WireWorkloadKind.CRON_JOB,
  job: WireWorkloadKind.JOB,
  unknown: WireWorkloadKind.UNSPECIFIED,
};
const WIRE_HEALTH: Record<WorkloadHealth, WireWorkloadHealth> = {
  healthy: WireWorkloadHealth.HEALTHY,
  degraded: WireWorkloadHealth.DEGRADED,
  crash_looping: WireWorkloadHealth.CRASH_LOOPING,
  pending: WireWorkloadHealth.PENDING,
  suspended: WireWorkloadHealth.SUSPENDED,
  unknown: WireWorkloadHealth.UNKNOWN,
};
const WIRE_CAUSE: Record<WorkloadCause, WireWorkloadCause> = {
  none: WireWorkloadCause.NONE,
  workload: WireWorkloadCause.WORKLOAD,
  platform: WireWorkloadCause.PLATFORM,
  undetermined: WireWorkloadCause.UNDETERMINED,
};
const WIRE_BASIS: Record<WorkloadReleaseBasis, WireWorkloadReleaseBasis> = {
  digest: WireWorkloadReleaseBasis.DIGEST,
  promotion: WireWorkloadReleaseBasis.PROMOTION,
  none: WireWorkloadReleaseBasis.UNSPECIFIED,
};
const WIRE_JOB_RESULT: Record<WorkloadJobResult, WireWorkloadJobResult> = {
  running: WireWorkloadJobResult.RUNNING,
  succeeded: WireWorkloadJobResult.SUCCEEDED,
  failed: WireWorkloadJobResult.FAILED,
  unknown: WireWorkloadJobResult.UNSPECIFIED,
};
const WIRE_POD_STATE = { running: WireWorkloadPodState.RUNNING, gone: WireWorkloadPodState.GONE, unknown: WireWorkloadPodState.UNSPECIFIED } as const;
const WIRE_SPEC_GAP: Record<DeclaredSpecGap, WireDeclaredSpecGap> = {
  pending: WireDeclaredSpecGap.PENDING,
  unreadable: WireDeclaredSpecGap.UNREADABLE,
  "not-in-manifests": WireDeclaredSpecGap.NOT_IN_MANIFESTS,
  unknown: WireDeclaredSpecGap.UNSPECIFIED,
};
const WIRE_WORKLOAD_METRIC: Record<WorkloadMetric, WireWorkloadMetric> = {
  cpu_usage: WireWorkloadMetric.CPU_USAGE,
  cpu_limit_utilization: WireWorkloadMetric.CPU_LIMIT_UTILIZATION,
  cpu_request_utilization: WireWorkloadMetric.CPU_REQUEST_UTILIZATION,
  memory_working_set: WireWorkloadMetric.MEMORY_WORKING_SET,
  memory_limit_utilization: WireWorkloadMetric.MEMORY_LIMIT_UTILIZATION,
  memory_request_utilization: WireWorkloadMetric.MEMORY_REQUEST_UTILIZATION,
  restarts: WireWorkloadMetric.RESTARTS,
};

const nullable = (v: number | null): number | undefined => (v === null ? undefined : v);

function wireUsage(u: ResourceUsage | null) {
  if (!u) return undefined;
  return {
    latest: u.latest,
    p95: u.p95,
    limitUtilizationLatest: nullable(u.limitUtilizationLatest),
    limitUtilizationP95: nullable(u.limitUtilizationP95),
    requestUtilizationLatest: nullable(u.requestUtilizationLatest),
    requestUtilizationP95: nullable(u.requestUtilizationP95),
    limit: nullable(u.limit),
    request: nullable(u.request),
  };
}

const wireEvent = (e: WorkloadEvent) => ({ ...e, time: ts(e.timeMs), count: int64(e.count) });

function wireDeclared(d: DeclaredWorkload) {
  return {
    name: d.name,
    kind: WIRE_WORKLOAD_KIND[d.kind],
    specRecorded: d.specRecorded,
    specGap: d.specGap ? WIRE_SPEC_GAP[d.specGap] : WireDeclaredSpecGap.UNSPECIFIED,
    replicas: nullable(d.replicas),
    cpuRequestMillicores: int64(d.cpuRequestMillicores),
    cpuLimitMillicores: int64(d.cpuLimitMillicores),
    memoryRequestBytes: int64(d.memoryRequestBytes),
    memoryLimitBytes: int64(d.memoryLimitBytes),
    ports: d.ports.map((p) => ({ ...p })),
    envNames: [...d.envNames],
    image: d.image,
    sourcePath: d.sourcePath,
    sourceRepo: d.sourceRepo,
    sourceCommit: d.sourceCommit,
    bundleId: d.bundleId,
  };
}

function wireWorkload(w: Workload) {
  return {
    envId: w.envId,
    envName: w.envName,
    workload: w.workload,
    namespace: w.namespace,
    serviceVersion: w.serviceVersion,
    replicasObserved: w.replicasObserved,
    restartsInWindow: int64(w.restartsInWindow),
    latestWarning: w.latestWarning ? wireEvent(w.latestWarning) : undefined,
    containers: w.containers.map((c) => ({
      container: c.container,
      imageName: c.imageName,
      imageTag: c.imageTag,
      restartsInWindow: int64(c.restartsInWindow),
      lastTerminatedReason: c.lastTerminatedReason,
      lastTerminatedAt: tsOpt(c.lastTerminatedAtMs),
      cpu: wireUsage(c.cpu),
      memory: wireUsage(c.memory),
    })),
    kind: WIRE_WORKLOAD_KIND[w.kind],
    health: WIRE_HEALTH[w.health],
    healthReason: w.healthReason,
    healthCause: WIRE_CAUSE[w.healthCause],
    healthDetail: w.healthDetail,
    readyReplicas: nullable(w.readyReplicas),
    desiredReplicas: nullable(w.desiredReplicas),
    releaseVersion: w.releaseVersion,
    releaseBasis: WIRE_BASIS[w.releaseBasis],
    pods: w.pods.map((p) => ({
      name: p.name,
      uid: p.uid,
      state: WIRE_POD_STATE[p.state],
      restarts: int64(p.restarts),
      lastExitReason: p.lastExitReason,
      lastExitAt: tsOpt(p.lastExitAtMs),
      node: p.node,
      firstSeen: tsOpt(p.firstSeenMs),
      lastSeen: tsOpt(p.lastSeenMs),
      ageIsLowerBound: p.ageIsLowerBound,
      latestWarning: p.latestWarning ? wireEvent(p.latestWarning) : undefined,
    })),
    declared: w.declared ? wireDeclared(w.declared) : undefined,
  };
}

function workloadHandlers(fixtures: ObservabilityApi, clock: PackClock) {
  return completed(ObservabilityService, mockService(ObservabilityService, {
    listWorkloads: async (req) => {
      const scope = modelScope(req.scope, clock);
      const res = await fixtures.listWorkloads({ ...scope, services: scope.services.length ? scope.services : undefined });
      return {
        truncated: res.truncated,
        workloads: res.workloads.map(wireWorkload),
        jobs: res.jobs.map((j) => ({
          ...j,
          result: WIRE_JOB_RESULT[j.result],
          startedAt: tsOpt(j.startedAtMs),
          finishedAt: tsOpt(j.finishedAtMs),
          durationSeconds: int64(j.durationSeconds),
        })),
        jobsTruncated: res.jobsTruncated,
        ...(res.unavailableSections?.length ? { unavailableSections: [...res.unavailableSections] } : {}),
      };
    },
    getWorkload: async (req) => {
      const res = await fixtures.getWorkload({ ...modelScope(req.scope, clock), workload: req.workload, container: req.container, stepSeconds: req.stepSeconds });
      return {
        stepSeconds: res.stepSeconds,
        truncated: res.truncated,
        series: res.series.map((s) => ({ envId: s.envId, container: s.container, metric: WIRE_WORKLOAD_METRIC[s.metric], unit: s.unit, points: points(s.timestamps, s.values) })),
        events: res.events.map(wireEvent),
        imageChanges: res.imageChanges.map((c) => ({ envId: c.envId, container: c.container, imageName: c.imageName, imageTag: c.imageTag, firstSeen: ts(c.firstSeenMs), lastSeen: ts(c.lastSeenMs) })),
        versionChanges: res.versionChanges.map((c) => ({ envId: c.envId, serviceVersion: c.serviceVersion, firstSeen: ts(c.firstSeenMs), lastSeen: ts(c.lastSeenMs) })),
        workloads: res.workloads.map(wireWorkload),
      };
    },
  }));
}

// ── The service graph ────────────────────────────────────────────────────────

const WIRE_NODE_KIND: Partial<Record<GraphNode["kind"], GraphNodeKind>> = {
  frontend: GraphNodeKind.FRONTEND,
  process: GraphNodeKind.PROCESS,
  rpc: GraphNodeKind.RPC,
  method: GraphNodeKind.INTERNAL_METHOD,
  database: GraphNodeKind.DATABASE,
  external: GraphNodeKind.EXTERNAL,
  broker: GraphNodeKind.BROKER,
  workflow: GraphNodeKind.WORKFLOW,
};
const WIRE_EDGE_KIND: Partial<Record<GraphEdge["kind"], GraphEdgeKind>> = {
  sync: GraphEdgeKind.SYNC,
  database: GraphEdgeKind.DATABASE,
  external: GraphEdgeKind.EXTERNAL,
  "async-send": GraphEdgeKind.ASYNC_SEND,
  "async-consume": GraphEdgeKind.ASYNC_CONSUME,
  internal: GraphEdgeKind.INTERNAL,
  declared: GraphEdgeKind.DECLARED,
};
const WIRE_EVIDENCE = { observed: GraphEvidenceKind.OBSERVED, declared: GraphEvidenceKind.DECLARED, unknown: GraphEvidenceKind.UNSPECIFIED } as const;

const wireHistogram = (h: GraphEdge["duration"]) =>
  h ? { version: h.version, upperBoundsSeconds: [...h.boundsSeconds], bucketCounts: [...h.buckets], count: h.count, sumSeconds: h.sumSeconds } : undefined;

function wireQuality(q: GraphQuality | null) {
  if (!q) return undefined;
  return {
    metricsOrigin: q.origin,
    upstreamLossUnknown: q.lossUnknown,
    upstreamSamplingUnknown: q.samplingUnknown,
    truncated: q.truncated,
    provisional: q.provisional,
    duplicateCount: q.duplicates,
    conflictingCount: q.conflicts,
    unmatchedCount: q.unmatched,
    rejectedCount: q.rejected,
    overflowCount: q.overflow,
    aggregatedThrough: tsOpt(q.aggregatedThroughMs),
    warnings: [...q.warnings],
    availableMinutes: q.availableMinutes,
    requestedMinutes: q.requestedMinutes,
  };
}

function graphHandlers(fixtures: ObservabilityApi, clock: PackClock) {
  return completed(ObservabilityService, mockService(ObservabilityService, {
    getServiceGraph: async (req) => {
      const scope = modelScope(req.scope, clock);
      const res = await fixtures.getServiceGraph({
        ...scope,
        services: scope.services,
        rpcIds: [...req.rpcIds],
        rpcService: req.rpcService,
        includeInternal: req.includeInternal,
        includeDeclared: req.edgeKinds.includes(GraphEdgeKind.DECLARED),
      });
      return {
        nodes: res.nodes.map((n) => ({ id: n.id, envId: n.envId, kind: WIRE_NODE_KIND[n.kind] ?? GraphNodeKind.UNSPECIFIED, label: n.label, parentNodeId: n.parentId, rpcId: n.rpcId, sourceUnavailableReason: n.sourceUnavailableReason })),
        edges: res.edges.map((e) => ({
          id: e.id,
          sourceNodeId: e.source,
          destinationNodeId: e.target,
          envId: e.envId,
          kind: WIRE_EDGE_KIND[e.kind] ?? GraphEdgeKind.UNSPECIFIED,
          evidence: WIRE_EVIDENCE[e.evidence],
          rpcId: e.rpcId,
          unit: e.unit,
          viewpoint: e.viewpoint,
          cohort: e.cohort,
          count: e.count,
          serverFaultCount: e.faults,
          userOutcomeCount: e.userOutcomes,
          cancelledCount: e.cancelled,
          unknownStatusCount: e.unknownStatus,
          matchedCount: e.matched,
          unmatchedCount: e.unmatched,
          duration: wireHistogram(e.duration),
          exemplars: e.exemplars.map((x) => ({ traceId: x.traceId, spanId: x.spanId, traceAvailabilityUnknown: x.availabilityUnknown })),
          firstSeen: ts(e.firstSeenMs),
          lastSeen: ts(e.lastSeenMs),
        })),
        completeness: wireQuality(res.quality),
        effectiveTimeRange: res.window ? { start: ts(res.window.startMs), end: ts(res.window.endMs) } : undefined,
      };
    },
    getServiceGraphCatalog: async (req) => {
      const scope = modelScope(req.scope, clock);
      const res = await fixtures.getServiceGraphCatalog({ ...scope, services: scope.services, rpcService: req.rpcService, rpcIds: [...req.rpcIds] });
      return {
        methods: res.methods.map((m) => ({
          rpcId: m.id,
          rpcService: m.service,
          method: m.method,
          processNodeIds: [...m.processIds],
          evidence: WIRE_EVIDENCE[m.evidence],
          observedCount: m.observedCount,
          serverFaultCount: m.faults,
          userOutcomeCount: m.userOutcomes,
          duration: wireHistogram(m.duration),
          inputType: m.inputType,
          outputType: m.outputType,
          streamMode: m.streaming,
          sourceUnavailableReason: m.sourceUnavailableReason,
          idempotency: m.idempotency,
          authRequired: m.authRequired,
          servesProcessIds: [...m.servesProcessIds],
          canCallProcessIds: [...m.canCallProcessIds],
        })),
        declaredCatalogs: res.declaredCatalogs.map((c) => ({ envId: c.envId, releaseId: c.releaseId, bundleDigest: c.bundleDigest, catalogJson: c.json, repository: c.repository, commit: c.commit })),
        completeness: wireQuality(res.quality),
        declaredCatalogStatus: res.declaredStatus,
        declaredCatalogUnavailableReason: res.unavailableReason,
      };
    },
  }));
}

// ── Metrics ──────────────────────────────────────────────────────────────────

const WIRE_TEMPORALITY = { delta: WireTemporality.DELTA, cumulative: WireTemporality.CUMULATIVE, unknown: WireTemporality.UNSPECIFIED } as const;

const wireMetricInfo = (m: MetricCatalogEntry) => ({
  name: m.name,
  type: wireMetricType(m.kind),
  unit: m.unit,
  description: m.description,
  monotonic: m.monotonic,
  temporality: WIRE_TEMPORALITY[m.temporality],
  services: [...m.services],
  serviceCount: m.serviceCount,
  lastSeen: tsOpt(m.lastSeenMs),
  seriesEstimate: int64(m.seriesEstimate),
  queryable: m.queryable,
  builtin: m.builtin,
  declared: m.declared,
  seen: m.seen,
  stability: m.stability,
});

function metricHandlers(fixtures: ObservabilityApi, clock: PackClock) {
  return completed(ObservabilityService, mockService(ObservabilityService, {
    listMetrics: async (req) => {
      const res = await fixtures.listMetricCatalog({ ...modelScope(req.scope, clock), name: req.nameContains, limit: req.limit });
      return { metrics: res.metrics.map(wireMetricInfo), truncated: res.truncated };
    },
    getMetric: async (req) => {
      const res = await fixtures.getMetric({ ...modelScope(req.scope, clock), name: req.metricName, kind: modelMetricKind(req.metricType) });
      return {
        info: wireMetricInfo(res.info),
        intervalSeconds: res.intervalSeconds,
        mixedTemporality: res.mixedTemporality,
        attributes: res.attributes.map((a) => ({ ...a, enumerated: [...a.enumerated], distinctEstimate: int64(a.distinctEstimate) })),
        suggestedBreakdowns: [...res.suggestedBreakdowns],
      };
    },
    listMetricExemplars: async (req) => {
      const res = await fixtures.listMetricExemplars({
        ...modelScope(req.scope, clock),
        name: req.metricName,
        kind: modelMetricKind(req.metricType),
        where: req.where.map((p) => ({ field: p.field, op: p.op, value: p.value, values: [...p.values] })),
        group: { ...req.group },
        range: windowOf(req.timeRange, clock),
        minValue: req.minValue,
        limit: req.limit,
      });
      return { exemplars: res.map((e) => ({ time: ts(e.timeMs), value: e.value, traceId: e.traceId, spanId: e.spanId, attributes: { ...e.attributes }, traceStored: e.traceStored })) };
    },
  }));
}

// ── Release health ───────────────────────────────────────────────────────────

const WIRE_VERDICT = { pass: WireVerdict.PASS, fail: WireVerdict.FAIL, inconclusive: WireVerdict.INCONCLUSIVE, unknown: WireVerdict.UNSPECIFIED } as const;
const WIRE_FINDING: Record<ReleaseFindingCode, WireFindingCode> = {
  error_rate: WireFindingCode.ERROR_RATE,
  latency: WireFindingCode.LATENCY,
  new_issue: WireFindingCode.NEW_ISSUE,
  regressed_issue: WireFindingCode.REGRESSED_ISSUE,
  alert_firing: WireFindingCode.ALERT_FIRING,
  workload_restarts: WireFindingCode.WORKLOAD_RESTARTS,
};

const wireStats = (s: ServiceWindowStats | null) => (s ? { requests: int64(s.requests), errorRate: s.errorRate, p50Ms: s.p50Ms, p95Ms: s.p95Ms, p99Ms: s.p99Ms } : undefined);
const wireRange = (w: TimeWindow | null) => (w ? { start: ts(w.startMs), end: ts(w.endMs) } : undefined);

/** A ReleaseHealth on the wire (deliveryApi.ts mapReleaseHealth, run backwards). */
export function wireReleaseHealth(h: ReleaseHealth) {
  const p = h.promotion;
  return {
    promotion: p ? { id: p.promotionId, releaseVersion: p.version, gitCommit: p.gitCommit, promotedAt: ts(p.promotedAtMs), previousReleaseVersion: p.previousVersion } : undefined,
    before: wireRange(h.before),
    after: wireRange(h.after),
    afterComplete: h.afterComplete,
    services: h.services.map((s) => ({ service: s.service, before: wireStats(s.before), after: wireStats(s.after), versionShare: s.versionShare })),
    newIssues: h.newIssues.map(wireIssue),
    regressedIssues: h.regressedIssues.map(wireIssue),
    firingAlerts: h.firingAlerts.map((a) => ({ rule: a.rule, fingerprint: a.fingerprint, firingAt: ts(a.firingAtMs), labels: { ...a.labels } })),
    workloadFindings: h.workloadFindings.map((w) => ({ workload: w.workload, restarts: int64(w.restarts), lastTerminatedReason: w.lastTerminatedReason })),
    verdict: WIRE_VERDICT[h.verdict],
    findings: h.findings.map((f) => ({ code: f.code ? WIRE_FINDING[f.code] : WireFindingCode.UNSPECIFIED, service: f.service, value: f.value, threshold: f.threshold, policy: f.policy, message: f.message })),
    thresholds: h.thresholds.map((t) => ({ ...t })),
    notEvaluated: [...h.notEvaluated],
  };
}

function releaseHealthHandlers(fixtures: ObservabilityApi) {
  return completed(ObservabilityService, mockService(ObservabilityService, {
    getReleaseHealth: async (req) => {
      const res = await fixtures.getReleaseHealth({
        orgId: req.scope?.orgId ?? "",
        projectId: req.scope?.projectId,
        envIds: [...(req.scope?.envIds ?? [])],
        promotionId: req.subject.case === "promotionId" ? req.subject.value : undefined,
        windowMs: msOfDuration(req.window),
      });
      return wireReleaseHealth(res);
    },
  }));
}
