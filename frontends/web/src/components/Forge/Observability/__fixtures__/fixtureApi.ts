// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory ObservabilityApi: deterministic telemetry for a small forge
 * app (an `api` service with an orders rpc set, a `worker`, and Reliant's own
 * daemon logs) across three environments (dev, staging, prod), shaped
 * exactly like what api.ts maps out of control-plane — including group-by,
 * attribute values and the customer metadata of its workloads.
 *
 * Shared by the component tests and the dev-only preview route, so the screens
 * a reviewer looks at are the screens the tests assert on. Every generator is
 * relative to the request's window, so "last hour" always has data in it.
 */

import { fixtureMetricCatalog, fixtureMetricDetail, fixtureMetricEntry, fixtureMetricExemplars, fixtureMetricPanel } from "./metricFixtures";
import { Code, ConnectError } from "@connectrpc/connect";

import forgeSpecRaw from "./forge-dashboard-spec.json?raw";
import { createAlertingFixture, type AlertingFixtureOptions } from "./fixtureAlerting";
import { createDashboardFixture, type DashboardFixtureOptions } from "./fixtureDashboards";
import { createDeliveryFixture, versionAt, type DeliveryFixtureOptions } from "./fixtureDelivery";
import { createIssuesFixture, type IssuesFixtureOptions } from "./fixtureIssues";
import { createSessionsFixture, type SessionsFixtureOptions } from "./fixtureSessions";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { fixtureGraph, fixtureCatalog } from "./fixtureGraph";
import type {
  AttributeValueCount,
  Dashboard,
  ObsScope,
  ResourceUsage,
  Workload,
  WorkloadDetail,
  WorkloadHealth,
  WorkloadEvent,
  WorkloadJob,
  WorkloadMetric,
  WorkloadSeries,
  Flamegraph,
  LogRecord,
  ObservabilityUsage,
  Panel,
  PanelResult,
  SeriesIntent,
  ServiceHealth,
  ServiceSignalsResult,
  Severity,
  Span,
  TimeWindow,
  Trace,
  TraceSummary,
} from "@/services/controlPlane/observability/types";

export const FIXTURE_ORG = "org-fixture";
export const FIXTURE_PROJECT = "shop";
export const FIXTURE_ENVS = [
  { id: "env-dev", name: "dev" },
  { id: "env-staging", name: "staging" },
  { id: "env-prod", name: "prod" },
] as const;
const VERSIONS = ["v1.8.2", "v1.8.3"];
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

/** The environments a scope reads: its ids, or every fixture environment. */
function envsOf(scope: Pick<ObsScope, "envIds">) {
  return scope.envIds.length ? FIXTURE_ENVS.filter((env) => scope.envIds.includes(env.id)) : [...FIXTURE_ENVS];
}

/** The values a key takes in the fixture app, for group-by and autocomplete. */
function valuesOf(key: string, scope: Pick<ObsScope, "envIds">): string[] {
  switch (key) {
    case "deployment.environment.name":
      return envsOf(scope).map((env) => env.name);
    case "reliant.env.id":
      return envsOf(scope).map((env) => env.id);
    case "service.name":
    case "k8s.workload.name":
      return ["api", "worker"];
    case "service.version":
      return VERSIONS;
    case "status":
      return ["ok", "error", "unset"];
    case "rpc.method":
      return RPCS;
    case "k8s.container.last_terminated_reason":
      return ["OOMKilled", "Error", "Completed"];
    case "k8s.event.reason":
      return ["BackOff", "OOMKilling", "Pulled", "Scheduled", "Evicted"];
    default:
      return ["value-a", "value-b"];
  }
}

/** Every combination of the keys' values: the groups a grouped query answers with. */
export function fixtureGroups(keys: string[], scope: Pick<ObsScope, "envIds">): Record<string, string>[] {
  return keys.reduce<Record<string, string>[]>(
    (groups, key) => groups.flatMap((group) => valuesOf(key, scope).map((value) => ({ ...group, [key]: value }))),
    [{}]
  );
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
    group: {},
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

/** forge's per-rpc RED dashboards for the fixture app: an overview and the api service's own. */
export function fixtureDashboards(): Dashboard[] {
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
      { id: "error-logs", title: "Recent error logs", kind: "search", signal: "logs", where: [{ field: "log.severity", op: "eq", value: "error" }], series: [], columns: ["timestamp", "service.name", "log.severity", "log.body"], width: 12, height: 7 },
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
  return [overview, service];
}

/**
 * The dashboards a release bundle carries: forge's own generated set (its
 * golden testdata, so the workloads and releases dashboards render exactly as
 * forge writes them) and two the project declares in its repo — a checkout
 * dashboard, and a frontend one with the browser kinds (funnel, issues,
 * sessions).
 */
export function fixtureReleaseDashboards(): { generated: Dashboard[]; repo: Dashboard[] } {
  const forge = JSON.parse(forgeSpecRaw) as { dashboards: (Dashboard & { annotations?: { source: string; label?: string }[] })[] };
  const generated: Dashboard[] = forge.dashboards
    .filter((d) => ["overview", "workloads", "releases", "service-orders"].includes(d.id))
    .map((d) => ({ ...d, filters: d.filters ?? [], annotations: d.annotations ?? [] }));
  const checkout: Dashboard = {
    id: "checkout",
    title: "Checkout funnel",
    description: "The project's own dashboard: deploy/observability/dashboards/checkout.json.",
    filters: [{ id: "service", label: "service.name", field: "service.name" }],
    annotations: [{ source: "releases", label: "Releases" }],
    panels: [
      { id: "orders", title: "Orders created", kind: "line", signal: "traces", where: [{ field: "span.name", op: "eq", value: PROCEDURE("CreateOrder") }], series: [{ label: "orders", agg: "count" }], unit: "count", width: 12, height: 7 },
      { id: "checkout-p95", title: "Checkout p95", kind: "number", signal: "traces", where: [{ field: "span.name", op: "eq", value: PROCEDURE("CreateOrder") }], series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }], unit: "ms", width: 12, height: 4 },
    ],
  };
  const frontend: Dashboard = {
    id: "frontend-web",
    title: "Frontend · web",
    description: "The storefront as its visitors see it: errors grouped by cause, sessions, and the checkout funnel.",
    filters: [{ id: "service", label: "service.name", field: "service.name" }],
    annotations: [{ source: "releases", label: "Releases" }],
    panels: [
      { id: "funnel-checkout", title: "Checkout funnel", description: "Product events in order, within 1h, counted per session.", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["view_product", "add_to_cart", "begin_checkout", "purchase"], window: "1h", by: "session" }, width: 12, height: 8 },
      { id: "js-errors", title: "JS errors by release", kind: "line", signal: "traces", where: [{ field: "span.status", op: "eq", value: "error" }], series: [{ label: "errors", agg: "count" }], groupBy: ["resource.attr.service.version"], unit: "count", width: 12, height: 8 },
      { id: "issues", title: "Issues", description: "Errors grouped by cause: new and regressed first, then the most frequent.", kind: "issues", signal: "traces", series: [], width: 24, height: 9 },
      { id: "sessions-with-errors", title: "Sessions with errors", description: "Sessions that hit an error, newest first.", kind: "sessions", signal: "traces", series: [], hasError: true, width: 24, height: 8 },
    ],
  };
  return { generated, repo: [checkout, frontend] };
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
 * The release comparison's resources table (ReleaseComparison RESOURCES_PANEL):
 * each side's version is the one the fixture ledger says was running then, and
 * v1.8.3 uses a little more of everything on `api`, so a comparison of the
 * newest prod release has something to show. (Requests, errors and latency come
 * from the release-health fixture, not from panels.)
 */
export function fixtureComparePanel(panel: Panel, window: TimeWindow, envIds: string[], ledger: Parameters<typeof versionAt>[0]): PanelResult {
  const side = panel.id.endsWith("-after") ? "after" : "before";
  const envId = envIds[0] ?? "env-prod";
  const running = versionAt(ledger, envId, side === "after" ? window.startMs + 1 : window.endMs - 1) ?? "v1.8.2";
  const regress = side === "after" && running === "v1.8.3";
  const columns = panel.series.map((s) => s.label);
  const workload = (name: string, cpu: number, memory: number) => ({ group: { "resource.attr.k8s.workload.name": name }, values: [cpu, memory] });
  return {
    kind: "table",
    unit: panel.unit ?? "",
    stepSeconds: 0,
    truncated: false,
    series: [],
    ...NO_BROWSER_ROWS,
    ...NO_METRIC_EXTRAS,
    columns,
    rows: [workload("api", regress ? 0.24 : 0.21, regress ? 216 * 2 ** 20 : 182 * 2 ** 20), workload("worker", 0.08, 96 * 2 ** 20)],
  };
}

const NO_BROWSER_ROWS: Pick<PanelResult, "funnelSteps" | "issues" | "sessions"> = { funnelSteps: [], issues: [], sessions: [] };

/** What only a metrics panel fills in: its series metadata, the pre-cap series count, warnings, and explain output. */
const NO_METRIC_EXTRAS: Pick<PanelResult, "seriesMeta" | "seriesTotal" | "warnings" | "compiledSql"> = { seriesMeta: [], seriesTotal: 0, warnings: [], compiledSql: [] };

/**
 * The browser kinds (C7) over the fixture app's `web` frontend: a checkout
 * funnel that leaks most between cart and payment, issues from the cart and
 * checkout pages (one regressed in the newest build), and sessions — some
 * with errors, some with a replay.
 */
export function fixtureBrowserPanel(panel: Panel, window: TimeWindow, services: string[]): Pick<PanelResult, "funnelSteps" | "issues" | "sessions"> {
  if (services.length && !services.includes("web")) return NO_BROWSER_ROWS;
  const span = window.endMs - window.startMs;
  const scale = Math.max(1, Math.round(span / 3_600_000));
  if (panel.kind === "funnel") {
    const steps = panel.funnel?.steps?.length ? panel.funnel.steps : ["view_product", "add_to_cart", "begin_checkout", "purchase"];
    const keep = [1, 0.46, 0.31, 0.72, 0.8, 0.9, 0.95, 0.97, 0.98, 0.99];
    let count = 1840 * scale;
    const first = count;
    let previous = count;
    return {
      ...NO_BROWSER_ROWS,
      funnelSteps: steps.map((event, index) => {
        count = Math.round(count * (index === 0 ? 1 : keep[index] ?? 0.9));
        const step = { event, count, rateFromFirst: first ? count / first : 0, rateFromPrevious: index === 0 ? 1 : previous ? count / previous : 0 };
        previous = count;
        return step;
      }),
    };
  }
  if (panel.kind === "issues") {
    const issue = (n: number, type: string, message: string, url: string, version: string, events: number, sessions: number, firstAgo: number) => ({
      serviceName: "web",
      stackHash: String(1_844_674_407_370_955_161n + BigInt(n)),
      exceptionType: type,
      message,
      urlPath: url,
      serviceVersion: version,
      events: events * scale,
      sessions: sessions * scale,
      firstSeenMs: window.endMs - firstAgo,
      lastSeenMs: window.endMs - 60_000 * n,
      sampleTraceId: n === 1 ? FIXTURE_TRACE_ID : `${n}bf92f3577b34da6a3ce929d0e0e47${n}0`,
      sampleSessionId: `sess-${n}a8c2f`,
    });
    return {
      ...NO_BROWSER_ROWS,
      issues: [
        issue(1, "TypeError", "Cannot read properties of undefined (reading '<str>')", "/checkout/payment", VERSIONS[1], 212, 61, 40 * 60_000),
        issue(2, "ChunkLoadError", "Loading chunk <n> failed", "/cart", VERSIONS[1], 74, 52, 3 * 3_600_000),
        issue(3, "Error", "Request failed with status code <n>", "/orders", VERSIONS[0], 31, 9, 26 * 3_600_000),
      ],
    };
  }
  const session = (n: number, user: string, path: string, minutes: number, pages: number, errors: number, replay: boolean) => ({
    sessionId: `sess-${n}a8c2f${n}d91e`,
    serviceName: "web",
    serviceVersion: VERSIONS[n % 2],
    envId: "env-prod",
    userId: user,
    entryPath: path,
    startMs: window.endMs - n * 7 * 60_000,
    endMs: window.endMs - n * 7 * 60_000 + minutes * 60_000,
    pageViews: pages,
    interactions: pages * 6,
    errors,
    hasReplay: replay,
  });
  const all = [
    session(1, "u_8812", "/checkout/payment", 6, 5, 2, true),
    session(2, "", "/", 3, 3, 0, false),
    session(3, "u_1093", "/cart", 11, 9, 1, true),
    session(4, "u_5530", "/products/espresso-grinder", 2, 2, 0, false),
    session(5, "", "/orders", 4, 4, 1, false),
  ];
  return { ...NO_BROWSER_ROWS, sessions: panel.hasError ? all.filter((s) => s.errors > 0) : all };
}

/**
 * What QueryPanel would answer for a panel over the fixture app: one value per
 * series intent, shaped by the panel's kind and group-by.
 */
export function fixturePanel(
  panel: Panel,
  window: TimeWindow,
  services: string[],
  groupByOverride: string[] = [],
  scope: Pick<ObsScope, "envIds"> = { envIds: [] }
): PanelResult {
  const random = rng(panel.id.length * 31 + 7 + groupByOverride.length * 13);
  const timestamps = buckets(window);
  const sample = (intent: SeriesIntent, scale = 1): number => {
    if (intent.agg === "quantile") return Math.round((intent.quantile ?? 0.5) * 120 * scale * (0.8 + random() * 0.4) * 10) / 10;
    if ((intent.where ?? []).some((p) => p.value === "error")) return Math.round(4 * scale * random());
    return Math.round(300 * scale * (0.7 + random() * 0.6));
  };
  const groupBy = groupByOverride.length ? groupByOverride : (panel.groupBy ?? []);
  const groups: Record<string, string>[] =
    groupBy.length === 1 && groupBy[0] === "service.name" && services.length
      ? services.map((name) => ({ "service.name": name }))
      : groupBy.length === 1 && groupBy[0] === "span.name"
        ? RPCS.map((rpc) => ({ "span.name": PROCEDURE(rpc) }))
        : fixtureGroups(groupBy.map((key) => key.replace(/^(resource|metric|log|span)\.attr\./, "")), scope).map((group, i) =>
            Object.fromEntries(Object.entries(group).map(([k, v], j) => [groupBy[j] ?? k, v ?? String(i)]))
          );
  const base = { kind: panel.kind, unit: panel.unit ?? "", stepSeconds: 120, truncated: false, columns: [] as string[], rows: [] as PanelResult["rows"], ...NO_BROWSER_ROWS, ...NO_METRIC_EXTRAS };

  if (panel.kind === "funnel" || panel.kind === "issues" || panel.kind === "sessions") {
    return { ...base, series: [], ...fixtureBrowserPanel(panel, window, services) };
  }

  if (panel.kind === "table") {
    return {
      ...base,
      series: [],
      columns: panel.series.map((s) => s.label),
      rows: groups.map((group, index) => ({ group, values: panel.series.map((intent) => sample(intent, 1 / (index + 1))) })),
    };
  }
  if (panel.kind === "number") {
    // A window that ended a while ago (a comparison's previous period) reads a
    // little lower, so a change against it has something to say.
    const earlier = Date.now() - window.endMs > 60_000 ? 0.88 : 1;
    const value = (intent: SeriesIntent, scale: number) =>
      panel.ratio ? 0.012 * scale : sample(intent, (intent.agg === "quantile" ? 1 : 40) * scale * earlier);
    const intents = panel.ratio ? [{ label: "ratio", agg: "count" }] : panel.series;
    return {
      ...base,
      series: groups.flatMap((group, groupIndex) =>
        intents.map((intent) => ({ label: intent.label, group, timestamps: [], values: [], value: value(intent, 1 / (groupIndex + 1)) }))
      ),
    };
  }
  // Line: one series per (series × group); a ratio, one per group labelled "ratio".
  const intents: SeriesIntent[] = panel.ratio ? [{ label: "ratio", agg: "count" }] : panel.series;
  const before = Date.now() - window.endMs > 60_000 ? 0.85 : 1;
  return {
    ...base,
    series: groups.flatMap((group, groupIndex) =>
      intents.map((intent) => ({
        label: intent.label,
        group,
        timestamps,
        values: timestamps.map((_, i) =>
          panel.ratio ? Math.max(0, 0.01 + (i > 18 && i < 23 ? 0.04 : 0) * random()) : sample(intent, ((1 + Math.sin(i / 4) * 0.3) * before) / (groupIndex + 1))
        ),
        value: null,
      }))
    ),
  };
}

const GIB = 2 ** 30;

/** A mid-month org: 6.4 GB of the 10 GB allowance used, a little of it browser, some of it customer metadata. */
export function fixtureUsage(now: number = Date.now(), overrides: Partial<ObservabilityUsage> = {}): ObservabilityUsage {
  const d = new Date(now);
  const start = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1);
  const end = Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1);
  return {
    periodStartMs: start,
    periodEndMs: end,
    measuredThroughMs: now - 40 * 60_000,
    usedBytes: 6.4 * GIB,
    allowanceBytes: 10 * GIB,
    overageCents: 0,
    projectedBytes: 12.8 * GIB,
    projectedOverageCents: 70,
    rateCentsPerGb: "25.0000",
    state: "ok",
    usageState: "ok",
    funded: false,
    exempt: false,
    spendCapCents: 5000,
    spendCapReached: false,
    browserBytes: 0.5 * GIB,
    browserBilledBytes: 0.5 * GIB,
    browserCapBytes: 2 * GIB,
    metadataBytes: 0.9 * GIB,
    bySignal: [
      { signal: "logs", billedBytes: 3.1 * GIB, storedBytes: 3.1 * GIB, rows: 9_100_000 },
      { signal: "traces", billedBytes: 1.8 * GIB, storedBytes: 1.8 * GIB, rows: 2_400_000 },
      { signal: "metrics", billedBytes: 1.5 * GIB, storedBytes: 1.5 * GIB, rows: 31_000_000 },
      { signal: "profiles", billedBytes: 0, storedBytes: 0, rows: 0 },
    ],
    byService: [
      { service: "api", signal: "logs", billedBytes: 2.2 * GIB },
      { service: "api", signal: "traces", billedBytes: 1.8 * GIB },
      { service: "worker", signal: "logs", billedBytes: 0.9 * GIB },
      { service: "checkout", signal: "metrics", billedBytes: 0.9 * GIB },
    ],
    ...overrides,
  };
}

// ── Workloads (customer metadata) ────────────────────────────────────────────

function usage(latest: number, p95: number, limit: number | null, request: number | null): ResourceUsage {
  return {
    latest,
    p95,
    limit,
    request,
    limitUtilizationLatest: limit ? latest / limit : null,
    limitUtilizationP95: limit ? p95 / limit : null,
    requestUtilizationLatest: request ? latest / request : null,
    requestUtilizationP95: request ? p95 / request : null,
  };
}

const MiB = 1024 * 1024;

/** The server-side summary fields of a fixture workload (kind, the health verdict, replicas, release). */
function summary(
  health: WorkloadHealth,
  healthReason: string,
  ready: number,
  desired: number,
  release: string,
  fault?: { cause: Workload["healthCause"]; detail: string }
) {
  return {
    kind: "deployment" as const,
    health,
    healthReason,
    healthCause: fault?.cause ?? ("none" as const),
    healthDetail: fault?.detail ?? "",
    readyReplicas: ready,
    desiredReplicas: desired,
    releaseVersion: release,
    releaseBasis: "digest" as const,
    pods: [],
    declared: null,
  };
}

/**
 * Per environment, an `api` (prod's last restart was an OOMKill, its memory
 * near the limit), a `worker` (crash-looping in staging) and a `web` with no
 * limits set — the three shapes the Workloads view has to draw.
 */
/**
 * The fixture app's job runs, newest first, as ListWorkloads answers them:
 * in prod the search reindex keeps failing and the nightly report failed once
 * then recovered; the migration succeeds everywhere.
 */
export function fixtureJobs(scope: Pick<ObsScope, "envIds">, now: number): WorkloadJob[] {
  const run = (env: (typeof FIXTURE_ENVS)[number], workload: string, suffix: string, result: WorkloadJob["result"], hoursAgo: number, reason = ""): WorkloadJob => ({
    envId: env.id,
    envName: env.name,
    name: `${workload}-${suffix}`,
    workload,
    result,
    reason,
    releaseVersion: VERSIONS[1],
    startedAtMs: now - hoursAgo * 3_600_000 - 90_000,
    finishedAtMs: now - hoursAgo * 3_600_000,
    durationSeconds: 90,
  });
  return envsOf(scope)
    .flatMap((env) =>
      env.name === "prod"
        ? [
            run(env, "search-reindex", "c41f", "failed", 1, "BackoffLimitExceeded"),
            run(env, "nightly-report", "9a20", "succeeded", 3),
            run(env, "shop-migrate", "b9e1", "succeeded", 5),
            run(env, "search-reindex", "77d2", "failed", 7, "DeadlineExceeded"),
            run(env, "nightly-report", "41cc", "failed", 20, "BackoffLimitExceeded"),
          ]
        : [run(env, "shop-migrate", "b9e1", "succeeded", 6)]
    )
    .sort((a, b) => (b.finishedAtMs ?? 0) - (a.finishedAtMs ?? 0));
}

export function fixtureWorkloads(scope: Pick<ObsScope, "envIds">, now: number): Workload[] {
  return envsOf(scope).flatMap((env, envIndex): Workload[] => {
    const prod = env.name === "prod";
    const staging = env.name === "staging";
    const version = prod ? VERSIONS[1] : VERSIONS[envIndex % 2];
    const event = (reason: string, plainReason: string, message: string, minutesAgo: number, count: number, container: string, workload: string): WorkloadEvent => ({
      timeMs: now - minutesAgo * 60_000,
      envId: env.id,
      type: "Warning",
      reason,
      plainReason,
      message,
      count,
      objectKind: "Pod",
      objectName: `${workload}-7d9f8c-x${envIndex}`,
      container,
    });
    return [
      {
        envId: env.id,
        envName: env.name,
        workload: "api",
        namespace: `shop-${env.name}`,
        serviceVersion: version,
        replicasObserved: prod ? 3 : 1,
        restartsInWindow: prod ? 3 : 0,
        latestWarning: prod ? event("OOMKilling", "Ran out of memory", "Memory cgroup out of memory: Killed process 1 (api)", 14, 3, "api", "api") : null,
        ...summary(
          prod ? "degraded" : "healthy",
          prod ? "Restarted 3 times. It ran out of memory. Raise its memory or reduce its usage" : "",
          prod ? 3 : 1,
          prod ? 3 : 1,
          version,
          prod ? { cause: "workload", detail: "last exit OOMKilled; OOMKilling: Memory cgroup out of memory" } : undefined
        ),
        containers: [
          {
            container: "api",
            imageName: "ghcr.io/acme/shop-api",
            imageTag: version,
            restartsInWindow: prod ? 3 : 0,
            lastTerminatedReason: prod ? "OOMKilled" : "",
            lastTerminatedAtMs: prod ? now - 14 * 60_000 : null,
            cpu: usage(prod ? 0.42 : 0.08, prod ? 0.61 : 0.12, 1, 0.25),
            memory: usage(prod ? 497 * MiB : 180 * MiB, prod ? 510 * MiB : 210 * MiB, 512 * MiB, 256 * MiB),
          },
        ],
      },
      {
        envId: env.id,
        envName: env.name,
        workload: "worker",
        namespace: `shop-${env.name}`,
        serviceVersion: version,
        replicasObserved: 1,
        restartsInWindow: staging ? 11 : 0,
        latestWarning: staging ? event("BackOff", "Waiting before the next restart attempt", "Back-off restarting failed container worker in pod worker-7d9f8c-x1", 2, 11, "worker", "worker") : null,
        ...summary(
          staging ? "crash_looping" : "healthy",
          staging ? "Restarting repeatedly (11 times). The app exited with an error" : "",
          staging ? 0 : 1,
          1,
          version,
          staging ? { cause: "workload", detail: "last exit Error; BackOff: Back-off restarting failed container" } : undefined
        ),
        containers: [
          {
            container: "worker",
            imageName: "ghcr.io/acme/shop-worker",
            imageTag: version,
            restartsInWindow: staging ? 11 : 0,
            lastTerminatedReason: staging ? "Error" : "",
            lastTerminatedAtMs: staging ? now - 2 * 60_000 : null,
            cpu: usage(0.21, 0.34, 0.5, 0.1),
            memory: usage(140 * MiB, 160 * MiB, 384 * MiB, 128 * MiB),
          },
        ],
      },
      {
        envId: env.id,
        envName: env.name,
        workload: "web",
        namespace: `shop-${env.name}`,
        serviceVersion: version,
        replicasObserved: 2,
        restartsInWindow: 0,
        latestWarning: null,
        ...summary("healthy", "", 2, 2, version),
        containers: [
          {
            container: "web",
            imageName: "ghcr.io/acme/shop-web",
            imageTag: version,
            restartsInWindow: 0,
            lastTerminatedReason: "",
            lastTerminatedAtMs: null,
            cpu: usage(0.02, 0.05, null, null),
            memory: usage(64 * MiB, 70 * MiB, null, null),
          },
        ],
      },
    ];
  });
}

/** One workload's time series, events and rollouts, per environment in scope. */
export function fixtureWorkloadDetail(scope: Pick<ObsScope, "envIds">, workload: string, container: string | undefined, window: TimeWindow): WorkloadDetail {
  const rows = fixtureWorkloads(scope, window.endMs).filter((w) => w.workload === workload);
  const timestamps = buckets(window, 40);
  const series: WorkloadSeries[] = [];
  const events: WorkloadEvent[] = [];
  const versionChanges: WorkloadDetail["versionChanges"] = [];
  const imageChanges: WorkloadDetail["imageChanges"] = [];
  const pivot = window.startMs + (window.endMs - window.startMs) * 0.62;
  rows.forEach((row, rowIndex) => {
    const random = rng(rowIndex * 17 + workload.length);
    for (const c of row.containers) {
      if (container && c.container !== container) continue;
      const add = (metric: WorkloadMetric, unit: string, at: (i: number) => number | null) =>
        series.push({ envId: row.envId, container: c.container, metric, unit, timestamps, values: timestamps.map((_, i) => at(i)) });
      const wave = (base: number, i: number) => base * (0.75 + 0.2 * Math.sin(i / 5 + rowIndex) + random() * 0.1);
      add("cpu_usage", "{cpu}", (i) => (c.cpu ? wave(c.cpu.latest, i) : null));
      add("memory_working_set", "By", (i) => (c.memory ? Math.min(wave(c.memory.latest, i) * (0.85 + i / 260), c.memory.limit ?? Infinity) : null));
      if (c.cpu?.limit) add("cpu_limit_utilization", "1", (i) => wave(c.cpu!.latest, i) / c.cpu!.limit!);
      if (c.cpu?.request) add("cpu_request_utilization", "1", (i) => wave(c.cpu!.latest, i) / c.cpu!.request!);
      if (c.memory?.limit) add("memory_limit_utilization", "1", (i) => Math.min((wave(c.memory!.latest, i) * (0.85 + i / 260)) / c.memory!.limit!, 1));
      if (c.memory?.request) add("memory_request_utilization", "1", (i) => (wave(c.memory!.latest, i) * (0.85 + i / 260)) / c.memory!.request!);
      add("restarts", "{restart}", (i) => (c.restartsInWindow > 0 && i % Math.max(3, Math.floor(40 / c.restartsInWindow)) === 0 ? 1 : 0));
      imageChanges.push({ envId: row.envId, container: c.container, imageName: c.imageName, imageTag: VERSIONS[0], firstSeenMs: window.startMs, lastSeenMs: pivot });
      if (row.envName === "prod") {
        imageChanges.push({ envId: row.envId, container: c.container, imageName: c.imageName, imageTag: VERSIONS[1], firstSeenMs: pivot, lastSeenMs: window.endMs });
      }
    }
    versionChanges.push({ envId: row.envId, serviceVersion: VERSIONS[0], firstSeenMs: window.startMs, lastSeenMs: pivot });
    if (row.envName === "prod") versionChanges.push({ envId: row.envId, serviceVersion: VERSIONS[1], firstSeenMs: pivot, lastSeenMs: window.endMs });
    if (row.latestWarning) events.push(row.latestWarning);
    events.push(
      { timeMs: pivot + 30_000, envId: row.envId, type: "Normal", reason: "Pulled", plainReason: "Image fetched", message: `Successfully pulled image "${row.containers[0]?.imageName}:${row.serviceVersion}" in 2.1s`, count: 1, objectKind: "Pod", objectName: `${workload}-7d9f8c-x${rowIndex}`, container: row.containers[0]?.container ?? "" },
      { timeMs: pivot, envId: row.envId, type: "Normal", reason: "Scheduled", plainReason: "", message: `Successfully assigned ${row.namespace}/${workload}-7d9f8c-x${rowIndex} to node-a`, count: 1, objectKind: "Pod", objectName: `${workload}-7d9f8c-x${rowIndex}`, container: "" }
    );
    if (row.envName === "prod" && workload === "api") {
      events.push({ timeMs: pivot - 600_000, envId: row.envId, type: "Warning", reason: "Evicted", plainReason: "Moved to another machine", message: "The node was low on resource: memory. Container api was using 498Mi, request is 256Mi.", count: 1, objectKind: "Pod", objectName: "api-7d9f8c-old", container: "api" });
    }
  });
  events.sort((a, b) => b.timeMs - a.timeMs);
  return { series, events, imageChanges, versionChanges, stepSeconds: Math.round((window.endMs - window.startMs) / 1000 / 40), truncated: false, workloads: rows };
}

export interface FixtureOptions extends AlertingFixtureOptions, DeliveryFixtureOptions, DashboardFixtureOptions, IssuesFixtureOptions, SessionsFixtureOptions {
  /** Overrides on the usage fixture, to put the page in a given state. */
  usage?: Partial<ObservabilityUsage>;
  /** Every call fails with this (FailedPrecondition is the "not set up" org). */
  failWith?: Code;
  /** Every call fails with exactly this error. Read at call time, so a test may set or clear it later. */
  failError?: unknown;
  /** An environment that has sent nothing. */
  empty?: boolean;
  graphUnknown?: boolean;
  /** DEV-only negative cases for catalog review regressions. */
  graphReview?: "retained-denied" | "retained-unavailable" | "rpc-201" | "artifacts-21" | "unsupported";
  /** Called for each RPC, with its request — tests assert on what was asked. */
  onCall?: (rpc: string, request: unknown) => void;
  /**
   * GetServiceSignals' answer. Unset, the RPC answers Unimplemented, as a
   * control plane that predates it does: nothing is known of any service's
   * signals, so every view stays offered.
   */
  signals?: ServiceSignalsResult;
}

export function createFixtureApi(options: FixtureOptions = {}): ObservabilityApi {
  const call = async <T>(rpc: string, request: unknown, answer: () => T): Promise<T> => {
    options.onCall?.(rpc, request);
    if (options.failError !== undefined) throw options.failError;
    if (options.failWith !== undefined) {
      const message =
        options.failWith === Code.FailedPrecondition ? "observability is not set up for this organization yet" : "fixture failure";
      throw new ConnectError(message, options.failWith);
    }
    return answer();
  };
  let catalogCalls = 0;
  const delivery = createDeliveryFixture(options, call);
  return {
    currentOrgId: async () => FIXTURE_ORG,
    getServiceGraph: (req) => call("GetServiceGraph", req, () => fixtureGraph(options.empty, options.graphUnknown)),
    getServiceGraphCatalog: (req) => call("GetServiceGraphCatalog", req, () => {
      catalogCalls++;
      if (options.graphReview === "unsupported") throw new ConnectError("unknown procedure", Code.Unimplemented);
      if (catalogCalls > 1 && options.graphReview?.startsWith("retained-")) throw new ConnectError("catalog no longer readable", options.graphReview === "retained-denied" ? Code.PermissionDenied : Code.Unavailable);
      const result = fixtureCatalog(options.empty, options.graphUnknown);
      if (options.graphReview === "rpc-201") {
        const selectedId = "shop.v1.OrdersService/ZZMethod201";
        result.methods = req.rpcIds?.includes(selectedId) ? [{ ...result.methods[0], id: selectedId, method: "ZZMethod201" }] : [];
      }
      if (options.graphReview === "artifacts-21") {
        result.declaredCatalogs = Array.from({ length: 21 }, (_, i) => ({ ...result.declaredCatalogs[0], envId: `env-${i}`, bundleDigest: `bundle-${i}` }));
        const last = JSON.parse(result.declaredCatalogs[20].json);
        last.services[0].descriptor.Schemas["shop.v1.GetOrderRequest"] = [{ name: "different_schema_in_21st", kind: "int64" }];
        result.declaredCatalogs[20].json = JSON.stringify(last);
      }
      return result;
    }),
    getServiceOverview: (req) =>
      call("GetServiceOverview", req, () => {
        if (options.empty) return { services: [], trendStepSeconds: 60 };
        const trendStepSeconds = Math.round((req.window.endMs - req.window.startMs) / 1000 / 30);
        const groupBy = req.groupBy ?? [];
        const make = (scale: number, seed: number, group: Record<string, string>) => [
          { ...health("api", req.window, 42 * scale, 0.012 / scale, 3 + seed, 86 / scale), group },
          { ...health("worker", req.window, 6 * scale, 0.004, 5 + seed, 240), group },
        ];
        const all = groupBy.length
          ? fixtureGroups(groupBy, req).flatMap((group, index) => make(1 / (index + 1), index * 7, group))
          : make(1, 0, {});
        const services = req.services?.length ? all.filter((s) => req.services!.includes(s.name)) : all;
        const daemon: ServiceHealth = { ...health("reliant-daemon", req.window, 0, 0, 9, 0), requestCount: 0, requestRate: 0, errorLogCount: 3, source: "daemon", operatorData: true };
        return { services: req.services?.length || groupBy.length ? services : [...services, daemon], trendStepSeconds };
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
        if (options.empty) return { traces: [], nextCursor: "", red: null, redGroups: [] };
        let traces = traceSummaries(req.window, 40);
        if (req.rootSpanName) traces = traces.filter((t) => t.rootOperation.includes(req.rootSpanName));
        if (req.errorsOnly) traces = traces.filter((t) => t.errorCount > 0);
        if (req.minDurationMs) traces = traces.filter((t) => t.durationMs >= req.minDurationMs);
        const durations = traces.map((t) => t.durationMs).sort((a, b) => a - b);
        const at = (p: number) => durations[Math.min(durations.length - 1, Math.floor(p * durations.length))] ?? 0;
        const errors = traces.filter((t) => t.errorCount > 0).length;
        const red = {
          traceCount: traces.length,
          errorTraceCount: errors,
          ratePerSecond: traces.length / ((req.window.endMs - req.window.startMs) / 1000),
          errorRate: traces.length ? errors / traces.length : 0,
          p50Ms: at(0.5),
          p95Ms: at(0.95),
          p99Ms: at(0.99),
        };
        const redGroups = fixtureGroups(req.groupBy ?? [], req)
          .filter((group) => Object.keys(group).length > 0)
          .map((group, index) => ({
            group,
            red: { ...red, traceCount: Math.round(red.traceCount / (index + 1)), errorRate: red.errorRate * (index % 2 ? 2.4 : 0.6), p95Ms: red.p95Ms * (1 + index * 0.35) },
          }));
        return { traces: traces.slice(0, req.limit), nextCursor: "", red, redGroups };
      }),
    getTrace: (req) => call("GetTrace", req, () => fixtureTrace(req.traceId, req.window.endMs)),
    queryPanel: (req) =>
      call("QueryPanel", req, () => {
        const { panel } = req;
        const unsupported = panel.series.find((series) => series.agg === "count_distinct");
        if (unsupported) {
          throw new ConnectError(`aggregation "count_distinct" is not supported on field ${unsupported.field}`, Code.InvalidArgument);
        }
        if (panel.signal === "metrics" && panel.series.some((series) => fixtureMetricEntry(series.field ?? ""))) return fixtureMetricPanel(panel, req.window, req.window.endMs);
        if (panel.id.startsWith("compare-")) return fixtureComparePanel(panel, req.window, req.envIds, delivery.ledger);
        return fixturePanel(panel, req.window, req.services, req.groupBy, req);
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
    getServiceSignals: (req) =>
      call("GetServiceSignals", req, () => {
        if (!options.signals) throw new ConnectError("unknown procedure controlplane.v1.ObservabilityService/GetServiceSignals", Code.Unimplemented);
        return options.signals;
      }),
    getObservabilityUsage: (orgId) => call("GetObservabilityUsage", { orgId }, () => fixtureUsage(Date.now(), options.usage)),
    ...createDashboardFixture(options, call),
    ...createAlertingFixture(options, call),
    listReleases: delivery.listReleases,
    getDoraMetrics: delivery.getDoraMetrics,
    ...createIssuesFixture(options, call, delivery.ledger),
    ...createSessionsFixture(options, call),
    listMetricCatalog: (req) => call("ListMetrics", req, () => (options.empty ? { metrics: [], truncated: false } : fixtureMetricCatalog(req.window.endMs, req.name, req.limit))),
    getMetric: (req) =>
      call("GetMetric", req, () => {
        const detail = fixtureMetricDetail(req.name, req.window.endMs);
        if (!detail) throw new ConnectError(`metric "${req.name}" has no data in this window`, Code.NotFound);
        return detail;
      }),
    listMetricExemplars: (req) => call("ListMetricExemplars", req, () => fixtureMetricExemplars(req.window.endMs, req.limit)),
    listAttributeValues: (req) =>
      call("ListAttributeValues", req, () => {
        const values: AttributeValueCount[] = valuesOf(req.key, req)
          .filter((value) => value.toLowerCase().startsWith((req.prefix ?? "").toLowerCase()))
          .map((value, index) => ({ value, count: Math.round(4200 / (index + 1)) }));
        return { values: options.empty ? [] : values, truncated: false };
      }),
    listWorkloads: (req) =>
      call("ListWorkloads", req, () => {
        if (options.empty) return { workloads: [], truncated: false, jobs: [], jobsTruncated: false };
        const all = fixtureWorkloads(req, req.window.endMs);
        return { workloads: req.services?.length ? all.filter((w) => req.services!.includes(w.workload)) : all, truncated: false, jobs: fixtureJobs(req, req.window.endMs), jobsTruncated: false };
      }),
    getWorkload: (req) => call("GetWorkload", req, () => fixtureWorkloadDetail(req, req.workload, req.container, req.window)),
  };
}
