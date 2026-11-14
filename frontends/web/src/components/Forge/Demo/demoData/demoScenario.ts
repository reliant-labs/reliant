// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's `demo` scenario for forge's mock transport
 * (@reliantlabs/forge-web-runtime/mock-transport): the ObservabilityService
 * reads the forge journey walks, answered on the WIRE, as control-plane would
 * answer them.
 *
 * The data is the console's existing sample app (Observability/__fixtures__,
 * the same fixtures the component tests and the other shells' demo use),
 * encoded into the proto messages: each handler decodes the request into the
 * console's model, asks the fixture, and encodes the answer. So the demo host
 * runs the console's REAL client — api.ts, its mappers, the read scheduler —
 * over the mock transport, and swapping in the demo-data gateway
 * (demoTransport.ts, `passthrough: "first"`) is a config flip, not new code.
 *
 * Which RPCs are served here is DEMO_SCENARIO_RPCS; the rest of the console
 * (issues, sessions, workloads, alerting, dashboards, metrics, delivery) still
 * reads the model fixtures directly (DemoConsole), until each is encoded here
 * (research/2026-11-05-console-health/forge-mock-friction.md, item R1).
 */

import { create } from "@bufbuild/protobuf";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import type { MockScenario } from "@reliantlabs/forge-web-runtime/mock-transport";

import {
  AttributeOp as WireAttributeOp,
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
  type QueryScope,
  type SearchLogsRequest,
} from "@/gen/controlplane/services/observability/v1/observability_pb";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { AttributeOp, LogRecord, ObsScope, Severity, Span, TimeWindow, TraceRed, TraceSummary } from "@/services/controlPlane/observability/types";

import { createDemoFixtures } from "./busyApp";
import { consoleScenarioHandlers } from "./consoleScenario";
import { configScenarioHandlers } from "./configScenario";
import { inventoryScenarioHandlers } from "./inventoryScenario";
import { DEMO_DEFAULT_SERVICES, defaultHandlers, withMessageRequests } from "./demoServiceDefaults";
import { scopedToDemo } from "./demoScope";

const SERVICE = ObservabilityService.typeName;
const rpc = (method: string) => `${SERVICE}/${method}`;

/** The console methods the scenario serves, by ObservabilityApi key → RPC. */
export const DEMO_SCENARIO_RPCS = {
  listTraces: rpc("ListTraces"),
  getTrace: rpc("GetTrace"),
  searchLogs: rpc("SearchLogs"),
  getServiceOverview: rpc("GetServiceOverview"),
  getFlamegraph: rpc("GetFlamegraph"),
  getServiceSignals: rpc("GetServiceSignals"),
} as const satisfies Partial<Record<keyof ObservabilityApi, string>>;

export type DemoScenarioMethod = keyof typeof DEMO_SCENARIO_RPCS;

// ── Wire ↔ model ─────────────────────────────────────────────────────────────

/** Epoch ms → Timestamp, keeping the sub-millisecond part (api.ts's msOf reads it back). */
function ts(ms: number): Timestamp {
  const seconds = Math.floor(ms / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

function msOf(t: Timestamp | undefined): number {
  return t ? Number(t.seconds) * 1000 + t.nanos / 1e6 : 0;
}

const OP: Record<number, AttributeOp> = {
  [WireAttributeOp.EQ]: "eq",
  [WireAttributeOp.NEQ]: "neq",
  [WireAttributeOp.EXISTS]: "exists",
  [WireAttributeOp.IN]: "in",
  [WireAttributeOp.CONTAINS]: "contains",
};

/** A QueryScope as the console's ObsScope, window and services. */
export function modelScope(scope: QueryScope | undefined, now: number = Date.now()): ObsScope & { window: TimeWindow; services: string[] } {
  const start = msOf(scope?.timeRange?.start);
  const end = msOf(scope?.timeRange?.end);
  return {
    orgId: scope?.orgId ?? "",
    projectId: scope?.projectId,
    envIds: [...(scope?.envIds ?? [])],
    filters: (scope?.filters ?? []).map((f) => ({ key: f.key, op: OP[f.op] ?? "eq", value: f.value, values: [...f.values] })),
    window: end > start ? { startMs: start, endMs: end } : { startMs: now - 3_600_000, endMs: now },
    services: [...(scope?.services ?? [])],
  };
}

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

const points = (timestamps: number[], values: (number | null)[]) =>
  timestamps.map((t, i) => ({ time: ts(t * 1000), value: values[i] ?? 0, null: values[i] === null, partial: false }));

// ── The scenario ─────────────────────────────────────────────────────────────

/**
 * The `demo` scenario over `fixtures` (a model-level ObservabilityApi; the
 * sample app by default). Each handler returns a proto message (create()),
 * as a server would.
 */
export function createDemoScenario(fixtures: ObservabilityApi = createDemoFixtures()): MockScenario {
  const handlers = {
    [DEMO_SCENARIO_RPCS.listTraces]: async (req: ListTracesRequest) => {
      const scope = modelScope(req.scope);
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
      });
    },
    [DEMO_SCENARIO_RPCS.getTrace]: async (req: GetTraceRequest) => {
      const scope = modelScope(req.scope);
      const trace = await fixtures.getTrace({ ...scope, traceId: req.traceId });
      return create(GetTraceResponseSchema, {
        traceId: trace.traceId,
        spans: trace.spans.map(wireSpan),
        truncated: trace.truncated,
        relatedTraces: (trace.relatedTraces ?? []).map((r) => ({ ...r })),
        relatedTracesTruncated: trace.relatedTracesTruncated ?? false,
      });
    },
    [DEMO_SCENARIO_RPCS.searchLogs]: async (req: SearchLogsRequest) => {
      const scope = modelScope(req.scope);
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
      return create(SearchLogsResponseSchema, { logs: res.records.map(wireLog), nextPageToken: res.nextCursor });
    },
    [DEMO_SCENARIO_RPCS.getServiceOverview]: async (req: GetServiceOverviewRequest) => {
      const scope = modelScope(req.scope);
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
    [DEMO_SCENARIO_RPCS.getFlamegraph]: async (req: GetFlamegraphRequest) => {
      const scope = modelScope(req.scope);
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
    [DEMO_SCENARIO_RPCS.getServiceSignals]: async (req: GetServiceSignalsRequest) => {
      const res = await fixtures.getServiceSignals(modelScope(req.scope));
      return create(GetServiceSignalsResponseSchema, {
        truncated: res.truncated,
        services: res.services.map((s) => ({ service: s.service, emitting: [...s.emitting], declared: [...s.declared], declarationKnown: s.declarationKnown })),
      });
    },
  };
  // Dispatch, most specific first: the tour's encoded reads (above), the console's story
  // (consoleScenario.ts: Releases, Environments, Secrets, Domains, Activity), then forge's
  // generated defaults for every other read of the console's services (demoServiceDefaults.ts).
  // A read naming another project or environment is answered empty first (demoScope.ts).
  return { name: "demo", handlers: withMessageRequests(scopedToDemo({ ...defaultHandlers(), ...consoleScenarioHandlers(), ...configScenarioHandlers(), ...inventoryScenarioHandlers(), ...handlers }, DEMO_DEFAULT_SERVICES)) as unknown as MockScenario["handlers"] };
}
