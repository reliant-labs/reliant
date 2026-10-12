// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory ObservabilityApi: deterministic telemetry for a small forge
 * app (an `api` service with an orders rpc set, a `worker`, and Reliant's own
 * daemon logs), shaped exactly like what api.ts maps out of control-plane.
 *
 * Shared by the component tests and the dev-only preview route, so the screens
 * a reviewer looks at are the screens the tests assert on. Every generator is
 * relative to the request's window, so "last hour" always has data in it.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type {
  Dashboard,
  DashboardSpec,
  Flamegraph,
  LogRecord,
  Panel,
  PanelResult,
  SeriesIntent,
  ServiceHealth,
  Severity,
  Span,
  TimeWindow,
  Trace,
  TraceSummary,
} from "@/services/controlPlane/observability/types";

export const FIXTURE_ORG = "org-fixture";
export const FIXTURE_TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736";

const RPCS = ["CreateOrder", "GetOrder", "ListOrders", "CancelOrder"];
const PROCEDURE = (rpc: string) => `orders.v1.OrdersService/${rpc}`;

/** A cheap deterministic PRNG, so fixtures do not change between renders. */
function rng(seed: number) {
  let state = seed >>> 0;
  return () => {
    state = (state * 1664525 + 1013904223) >>> 0;
    return state / 0x1_0000_0000;
  };
}

function buckets(window: TimeWindow, count = 30): number[] {
  const step = (window.endMs - window.startMs) / count;
  return Array.from({ length: count }, (_, i) => Math.round((window.startMs + i * step) / 1000));
}

function health(name: string, window: TimeWindow, base: number, errorShare: number, seed: number, p95: number): ServiceHealth {
  const random = rng(seed);
  const timestamps = buckets(window);
  const step = (window.endMs - window.startMs) / 1000 / timestamps.length;
  const requestTrend = timestamps.map((_, i) => Math.round(base * step * (0.7 + 0.5 * Math.sin(i / 4) * 0.5 + random() * 0.3)));
  // A burst of errors two-thirds of the way through: a shape worth seeing.
  const errorTrend = requestTrend.map((r, i) => Math.round(r * errorShare * (i > 18 && i < 23 ? 6 : 1) * random()));
  const requestCount = requestTrend.reduce((a, b) => a + b, 0);
  const errorCount = errorTrend.reduce((a, b) => a + b, 0);
  return {
    name,
    requestCount,
    requestRate: requestCount / ((window.endMs - window.startMs) / 1000),
    errorCount,
    errorRate: requestCount ? errorCount / requestCount : 0,
    p50Ms: p95 / 3.2,
    p95Ms: p95,
    p99Ms: p95 * 2.4,
    errorLogCount: Math.round(errorCount * 1.3),
    timestamps,
    requestTrend,
    errorTrend,
    rateSeries: requestTrend.map((r) => r / step),
    errorRateSeries: requestTrend.map((r, i) => (r ? errorTrend[i] / r : null)),
    source: "",
    operatorData: false,
  };
}

function logs(window: TimeWindow, count: number): LogRecord[] {
  const random = rng(7);
  const levels: [Severity, string][] = [
    ["info", "INFO"],
    ["info", "INFO"],
    ["debug", "DEBUG"],
    ["warn", "WARN"],
    ["error", "ERROR"],
  ];
  const messages = [
    "rpc handled",
    "order created",
    "cache miss for customer profile",
    "slow query: SELECT * FROM orders WHERE customer_id = $1 (412ms)",
    "payment provider timeout after 3 retries",
  ];
  return Array.from({ length: count }, (_, i) => {
    const pick = Math.floor(random() * levels.length);
    const [severity, severityText] = levels[pick];
    const service = i % 7 === 0 ? "worker" : "api";
    const traced = i % 3 !== 2;
    return {
      id: `fixture:${i}`,
      timeMs: window.endMs - (i + 1) * ((window.endMs - window.startMs) / (count + 1)),
      severity,
      severityText,
      service,
      body: messages[pick],
      traceId: traced ? (i === 0 ? FIXTURE_TRACE_ID : `${(i * 2654435761).toString(16).padStart(8, "0")}b34da6a3ce929d0e0e4736`.slice(0, 32)) : "",
      spanId: traced ? `00f067aa0ba902b${i % 10}` : "",
      attributes: { "rpc.method": RPCS[i % RPCS.length], procedure: `/${PROCEDURE(RPCS[i % RPCS.length])}`, "http.status_code": severity === "error" ? "503" : "200" },
      resource: { "service.name": service, "k8s.pod.name": `${service}-7d9f8c-x${i % 3}`, "deployment.environment.name": "prod" },
      source: "",
      operatorData: false,
    };
  });
}

function traceSummaries(window: TimeWindow, count: number): TraceSummary[] {
  const random = rng(11);
  return Array.from({ length: count }, (_, i) => {
    const rpc = RPCS[i % RPCS.length];
    const failed = i % 9 === 4;
    // The root span's own duration; every fifth trace has async work that outlives it.
    const root = Math.round((rpc === "ListOrders" ? 180 : 40) * (0.5 + random() * 2) * 100) / 100;
    return {
      traceId: i === 0 ? FIXTURE_TRACE_ID : `${(i * 40503).toString(16).padStart(6, "0")}77b34da6a3ce929d0e0e47${(i % 100).toString().padStart(2, "0")}`.slice(0, 32),
      rootService: "api",
      rootOperation: PROCEDURE(rpc),
      startMs: window.endMs - (i + 1) * 9_000,
      durationMs: root + (i % 5 === 0 ? 120 : 0),
      rootDurationMs: root,
      spanCount: 4 + (i % 5),
      errorCount: failed ? 1 : 0,
      services: ["api", "worker"],
      source: "",
      operatorData: false,
    };
  });
}

/** One trace: an rpc that calls the database twice and enqueues work a worker picks up, with one failed span. */
export function fixtureTrace(traceId: string, now: number): Trace {
  const t0 = now - 60_000;
  const span = (spanId: string, parentSpanId: string, name: string, service: string, start: number, duration: number, extra: Partial<Span> = {}): Span => ({
    spanId,
    parentSpanId,
    name,
    service,
    kind: parentSpanId ? "client" : "server",
    startMs: t0 + start,
    durationMs: duration,
    status: "ok",
    statusMessage: "",
    attributes: { "rpc.system": "connect_rpc" },
    resource: { "service.name": service, "service.version": "v1.7.15" },
    events: [],
    source: "",
    operatorData: false,
    ...extra,
  });
  return {
    traceId,
    truncated: false,
    spans: [
      span("a1", "", PROCEDURE("CreateOrder"), "api", 0, 182.4, { kind: "server", attributes: { "rpc.method": "CreateOrder", "http.status_code": "200" } }),
      span("b1", "a1", "SELECT orders", "api", 3.1, 12.6, { attributes: { "db.system": "postgresql", "db.operation": "SELECT" } }),
      span("b2", "a1", "INSERT orders", "api", 17.2, 8.9, { attributes: { "db.system": "postgresql", "db.operation": "INSERT" } }),
      span("b3", "a1", "payments.v1.PaymentsService/Charge", "api", 28.0, 120.5, {
        status: "error",
        statusMessage: "unavailable: payment provider timeout",
        events: [{ timeMs: t0 + 88, name: "retry", attributes: { attempt: "2" } }],
      }),
      span("c1", "b3", "POST https://payments.example/charge", "api", 30.2, 40.1),
      span("c2", "b3", "POST https://payments.example/charge", "api", 88.5, 58.2, { status: "error", statusMessage: "context deadline exceeded" }),
      span("b4", "a1", "nats publish orders.created", "api", 150.0, 0.8, { kind: "producer" }),
      span("d1", "b4", "orders.created process", "worker", 152.3, 28.7, { kind: "consumer" }),
      span("d2", "d1", "UPDATE inventory", "worker", 155.0, 21.4, { attributes: { "db.system": "postgresql", "db.operation": "UPDATE" } }),
    ],
  };
}

function rpcPanels(rpc: string): Panel[] {
  const where = [
    { field: "span.kind", op: "eq", value: "server" },
    { field: "span.name", op: "eq", value: PROCEDURE(rpc) },
  ];
  return [
    { id: `rpc-${rpc}-rate`, title: `${rpc}: requests`, kind: "line", signal: "traces", where, series: [{ label: "requests", agg: "count" }], unit: "count", width: 8, height: 6 },
    {
      id: `rpc-${rpc}-errors`,
      title: `${rpc}: error rate`,
      kind: "line",
      signal: "traces",
      where,
      ratio: true,
      series: [
        { label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] },
        { label: "requests", agg: "count" },
      ],
      unit: "percent",
      width: 8,
      height: 6,
    },
    {
      id: `rpc-${rpc}-latency`,
      title: `${rpc}: latency`,
      kind: "line",
      signal: "traces",
      where,
      series: [
        { label: "p50", agg: "quantile", quantile: 0.5, field: "span.duration" },
        { label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" },
        { label: "p99", agg: "quantile", quantile: 0.99, field: "span.duration" },
      ],
      unit: "ms",
      width: 8,
      height: 6,
    },
  ];
}

export function fixtureDashboards(): DashboardSpec {
  const rpcServer = [
    { field: "span.kind", op: "eq", value: "server" },
    { field: "span.name", op: "match", value: "^[A-Za-z0-9_.]+/[A-Za-z0-9_]+$" },
  ];
  const redTable = [
    { label: "requests", agg: "count" },
    { label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] },
    { label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" },
  ];
  const filters = [
    { id: "service", label: "service.name", field: "service.name" },
    { id: "env", label: "deployment.environment.name", field: "deployment.environment.name" },
  ];
  const overview: Dashboard = {
    id: "overview",
    title: "shop: overview",
    description: "RED for every rpc server span.",
    filters,
    panels: [
      { id: "requests", title: "Requests", kind: "number", signal: "traces", where: rpcServer, series: [{ label: "requests", agg: "count" }], unit: "count", width: 6, height: 4 },
      { id: "errors", title: "Server errors", kind: "number", signal: "traces", where: [...rpcServer, { field: "span.status", op: "eq", value: "error" }], series: [{ label: "errors", agg: "count" }], unit: "count", width: 6, height: 4 },
      { id: "p95", title: "p95 latency", kind: "number", signal: "traces", where: rpcServer, series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }], unit: "ms", width: 6, height: 4 },
      { id: "p99", title: "p99 latency", kind: "number", signal: "traces", where: rpcServer, series: [{ label: "p99", agg: "quantile", quantile: 0.99, field: "span.duration" }], unit: "ms", width: 6, height: 4 },
      { id: "request-rate", title: "Requests per service", kind: "line", signal: "traces", where: rpcServer, series: [{ label: "requests", agg: "count" }], groupBy: ["service.name"], unit: "count", width: 12, height: 7 },
      {
        id: "error-rate",
        title: "Error rate per service",
        kind: "line",
        signal: "traces",
        where: rpcServer,
        ratio: true,
        series: [
          { label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] },
          { label: "requests", agg: "count" },
        ],
        groupBy: ["service.name"],
        unit: "percent",
        width: 12,
        height: 7,
      },
      { id: "red-by-service", title: "Rate, errors and latency by service", kind: "table", signal: "traces", where: rpcServer, series: redTable, groupBy: ["service.name"], width: 24, height: 8 },
      { id: "go-goroutines", title: "Goroutines", kind: "line", signal: "metrics", series: [{ label: "goroutines", agg: "sum", field: "go.goroutine.count", metricType: "sum" }], unit: "count", width: 12, height: 7 },
      { id: "sessions", title: "Browser sessions", kind: "number", signal: "traces", series: [{ label: "sessions", agg: "count_distinct", field: "resource.attr.rum.sessionId" }], unit: "count", width: 12, height: 4 },
      { id: "error-logs", title: "Recent error logs", kind: "search", signal: "logs", where: [{ field: "log.severity", op: "eq", value: "error" }], series: [], width: 12, height: 7 },
    ],
  };
  const service: Dashboard = {
    id: "service-api",
    title: "shop: api service",
    description: "RED per rpc of orders.v1.OrdersService.",
    filters,
    panels: [
      { id: "red-by-rpc", title: "Rate, errors and latency by rpc", kind: "table", signal: "traces", where: [...rpcServer, { field: "span.name", op: "prefix", value: "orders.v1.OrdersService/" }], series: redTable, groupBy: ["span.name"], width: 24, height: 8 },
      ...RPCS.slice(0, 2).flatMap(rpcPanels),
    ],
  };
  return { version: "forge.dashboards/v1", project: "shop", source: "synthesized", dashboards: [overview, service] };
}

/** A small speedscope "sampled" profile: an rpc handler, JSON encoding and a database call. */
export function fixtureSpeedscope(): string {
  const frames = ["main", "net/http.(*conn).serve", "orders.(*Service).CreateOrder", "encoding/json.Marshal", "database/sql.(*DB).QueryContext", "runtime.mallocgc", "syscall.Syscall"].map((name) => ({ name }));
  const samples = [
    [0, 1, 2, 3],
    [0, 1, 2, 3, 5],
    [0, 1, 2, 4, 6],
    [0, 1, 2, 4, 6],
    [0, 1, 2, 4],
    [0, 1, 2],
  ];
  const weights = [40e6, 25e6, 90e6, 60e6, 20e6, 10e6];
  return JSON.stringify({
    $schema: "https://www.speedscope.app/file-format-schema.json",
    shared: { frames },
    profiles: [{ type: "sampled", name: "api cpu", unit: "nanoseconds", startValue: 0, endValue: weights.reduce((a, b) => a + b, 0), samples, weights }],
    name: "api cpu",
    exporter: "reliant fixtures",
  });
}

/**
 * What QueryPanel would answer for a panel over the fixture app: one value per
 * series intent, shaped by the panel's kind and group-by.
 */
export function fixturePanel(panel: Panel, window: TimeWindow, services: string[]): PanelResult {
  const random = rng(panel.id.length * 31 + 7);
  const timestamps = buckets(window);
  const sample = (intent: SeriesIntent, scale = 1): number => {
    if (intent.agg === "quantile") return Math.round((intent.quantile ?? 0.5) * 120 * scale * (0.8 + random() * 0.4) * 10) / 10;
    if ((intent.where ?? []).some((p) => p.value === "error")) return Math.round(4 * scale * random());
    return Math.round(300 * scale * (0.7 + random() * 0.6));
  };
  const groupBy = panel.groupBy ?? [];
  const groups: Record<string, string>[] =
    groupBy[0] === "service.name"
      ? (services.length ? services : ["api", "worker"]).map((name) => ({ "service.name": name }))
      : groupBy[0] === "span.name"
        ? RPCS.map((rpc) => ({ "span.name": PROCEDURE(rpc) }))
        : groupBy.length
          ? [{ [groupBy[0]]: "value-a" }, { [groupBy[0]]: "value-b" }]
          : [{}];
  const base = { kind: panel.kind, unit: panel.unit ?? "", stepSeconds: 120, truncated: false, columns: [] as string[], rows: [] as PanelResult["rows"] };

  if (panel.kind === "table") {
    return {
      ...base,
      series: [],
      columns: panel.series.map((s) => s.label),
      rows: groups.map((group, index) => ({ group, values: panel.series.map((intent) => sample(intent, 1 / (index + 1))) })),
    };
  }
  if (panel.kind === "number") {
    const value = (intent: SeriesIntent) => (panel.ratio ? 0.012 : sample(intent, 40));
    const intents = panel.ratio ? [{ label: "ratio", agg: "count" }] : panel.series;
    return { ...base, series: intents.map((intent) => ({ label: intent.label, group: {}, timestamps: [], values: [], value: value(intent) })) };
  }
  // Line: one series per (series × group); a ratio, one per group labelled "ratio".
  const intents: SeriesIntent[] = panel.ratio ? [{ label: "ratio", agg: "count" }] : panel.series;
  return {
    ...base,
    series: groups.flatMap((group, groupIndex) =>
      intents.map((intent) => ({
        label: intent.label,
        group,
        timestamps,
        values: timestamps.map((_, i) =>
          panel.ratio ? Math.max(0, 0.01 + (i > 18 && i < 23 ? 0.04 : 0) * random()) : sample(intent, (1 + Math.sin(i / 4) * 0.3) / (groupIndex + 1))
        ),
        value: null,
      }))
    ),
  };
}

export interface FixtureOptions {
  /** Every call fails with this (FailedPrecondition is the "not set up" org). */
  failWith?: Code;
  /** An environment that has sent nothing. */
  empty?: boolean;
  /** Called for each RPC, with its request — tests assert on what was asked. */
  onCall?: (rpc: string, request: unknown) => void;
}

export function createFixtureApi(options: FixtureOptions = {}): ObservabilityApi {
  const call = async <T>(rpc: string, request: unknown, answer: () => T): Promise<T> => {
    options.onCall?.(rpc, request);
    if (options.failWith !== undefined) {
      const message =
        options.failWith === Code.FailedPrecondition ? "observability is not set up for this organization yet" : "fixture failure";
      throw new ConnectError(message, options.failWith);
    }
    return answer();
  };
  return {
    currentOrgId: async () => FIXTURE_ORG,
    getServiceOverview: (req) =>
      call("GetServiceOverview", req, () => {
        if (options.empty) return { services: [], trendStepSeconds: 60 };
        const all = [health("api", req.window, 42, 0.012, 3, 86), health("worker", req.window, 6, 0.004, 5, 240)];
        const services = req.services?.length ? all.filter((s) => req.services!.includes(s.name)) : all;
        const daemon: ServiceHealth = { ...health("reliant-daemon", req.window, 0, 0, 9, 0), requestCount: 0, requestRate: 0, errorLogCount: 3, source: "daemon", operatorData: true };
        return { services: req.services?.length ? services : [...services, daemon], trendStepSeconds: Math.round((req.window.endMs - req.window.startMs) / 1000 / 30) };
      }),
    searchLogs: (req) =>
      call("SearchLogs", req, () => {
        if (options.empty) return { records: [], nextCursor: "" };
        let records = logs(req.window, 60);
        if (req.services.length) records = records.filter((r) => req.services.includes(r.service));
        if (req.severities.length) records = records.filter((r) => req.severities.includes(r.severity));
        if (req.traceId) records = records.filter((r) => r.traceId === req.traceId);
        if (req.query) records = records.filter((r) => r.body.includes(req.query));
        return { records, nextCursor: req.cursor ? "" : records.length ? "page-2" : "" };
      }),
    listTraces: (req) =>
      call("ListTraces", req, () => {
        if (options.empty) return { traces: [], nextCursor: "", red: null };
        let traces = traceSummaries(req.window, 40);
        if (req.rootSpanName) traces = traces.filter((t) => t.rootOperation.includes(req.rootSpanName));
        if (req.errorsOnly) traces = traces.filter((t) => t.errorCount > 0);
        if (req.minDurationMs) traces = traces.filter((t) => t.durationMs >= req.minDurationMs);
        const durations = traces.map((t) => t.durationMs).sort((a, b) => a - b);
        const at = (p: number) => durations[Math.min(durations.length - 1, Math.floor(p * durations.length))] ?? 0;
        const errors = traces.filter((t) => t.errorCount > 0).length;
        return {
          traces: traces.slice(0, req.limit),
          nextCursor: "",
          red: {
            traceCount: traces.length,
            errorTraceCount: errors,
            ratePerSecond: traces.length / ((req.window.endMs - req.window.startMs) / 1000),
            errorRate: traces.length ? errors / traces.length : 0,
            p50Ms: at(0.5),
            p95Ms: at(0.95),
            p99Ms: at(0.99),
          },
        };
      }),
    getTrace: (req) => call("GetTrace", req, () => fixtureTrace(req.traceId, req.window.endMs)),
    queryPanel: (req) =>
      call("QueryPanel", req, () => {
        const { panel } = req;
        const unsupported = panel.series.find((series) => series.agg === "count_distinct");
        if (unsupported) {
          throw new ConnectError(`aggregation "count_distinct" is not supported on field ${unsupported.field}`, Code.InvalidArgument);
        }
        return fixturePanel(panel, req.window, req.services);
      }),
    getFlamegraph: (req): Promise<Flamegraph> =>
      call("GetFlamegraph", req, () => ({
        speedscopeJson: options.empty ? "" : fixtureSpeedscope(),
        totalValue: options.empty ? 0 : 245e6,
        unit: "nanoseconds",
        stackCount: options.empty ? 0 : 6,
        availableProfileTypes: ["cpu", "alloc_space", "inuse_space", "goroutine"],
        truncated: false,
      })),
    getDashboards: (req) => call("GetDashboards", req, () => (options.empty ? { ...fixtureDashboards(), dashboards: [] } : fixtureDashboards())),
  };
}
