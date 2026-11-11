// Copyright (c) 2025 Reliant Labs

/**
 * shop's metrics: the catalog its services emit (the orders counters it
 * declares, RPC and payment latency, the Go runtime, the queue, the
 * containers), each with a value model in its own unit — bytes of memory,
 * goroutines, orders per second, seconds — derived from the story's traffic
 * where it is traffic (demoStats) and steady where it is a runtime level. A
 * metric the catalog lists always has data to chart, and nothing else does.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { MetricCatalogEntry, MetricDetail, MetricExemplar, MetricKind, SeriesMeta } from "@/services/controlPlane/observability/metricsTypes";
import { catalogGroup } from "@/services/controlPlane/observability/metricsTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { Panel, PanelResult, QueryPanelRequest, SeriesIntent, TimeWindow } from "@/services/controlPlane/observability/types";

import { failedIncidentRequests } from "../busyApp";
import { DEMO_BACKENDS, DEMO_ENV_IDS, DEMO_INCIDENT, dailyLoad, demoLedger, demoStats, incidentStartMs, MINUTE, type DemoEnvKey } from "../shopStory";
import type { MetricKeys } from "./shopApi";

const MiB = 2 ** 20;

interface MetricSpec {
  name: string;
  kind: MetricKind;
  unit: string;
  description: string;
  monotonic?: boolean;
  services: readonly string[];
  declared?: boolean;
  /**
   * The value of one series at `at` for one service and environment: a level
   * (gauge, up-down counter), a rate per second (monotonic counter), or —
   * for a histogram — the latency quantile `q` in the metric's unit.
   */
  value: (service: string, env: DemoEnvKey, at: number, q: number, now: number) => number;
}

/** Requests per second on `ops` (all when empty) of `service` around `at`. */
const rps = (service: string | undefined, env: DemoEnvKey, at: number, now: number, op?: string) =>
  demoStats({ startMs: at - 2.5 * MINUTE, endMs: at + 2.5 * MINUTE, envIds: [env], service, op, now }).ratePerSecond;
const errs = (env: DemoEnvKey, at: number, now: number) =>
  ["orders.v1.OrdersService/CreateOrder", "checkout.v1.CheckoutService/Checkout"].reduce((a, op) => {
    const s = demoStats({ startMs: at - 2.5 * MINUTE, endMs: at + 2.5 * MINUTE, envIds: [env], service: "api", op, now });
    return a + s.errors / 300;
  }, 0);
const quantileOf = (q: number, s: { p50Ms: number; p95Ms: number; p99Ms: number }) => (q >= 0.99 ? s.p99Ms : q >= 0.95 ? s.p95Ms : q >= 0.9 ? (s.p50Ms + s.p95Ms) / 2 : s.p50Ms);
const wiggle = (at: number, service: string) => 1 + 0.04 * Math.sin(at / 600_000 + service.length);
const load = (env: DemoEnvKey, at: number) => (env === "env-prod" ? dailyLoad(at) : 0.3);
const incidentOn = (env: DemoEnvKey, at: number, now: number) => env === "env-prod" && at >= incidentStartMs(now);

const BASE_MEMORY: Record<string, number> = { api: 186 * MiB, worker: 58 * MiB, inventory: 94 * MiB };
const BASE_GOROUTINES: Record<string, number> = { api: 74, worker: 11, inventory: 23 };
const BASE_CPU: Record<string, number> = { api: 0.24, worker: 0.05, inventory: 0.09 };
const CPU_REQUEST: Record<string, number> = { api: 0.25, worker: 0.1, inventory: 0.1 };
const MEMORY_LIMIT = 512 * MiB;
const MEMORY_REQUEST = 256 * MiB;

const ORDER_OPS = ["orders.v1.OrdersService/CreateOrder", "checkout.v1.CheckoutService/Checkout"];

const CATALOG: MetricSpec[] = [
  {
    name: "orders.created",
    kind: "sum",
    unit: "{order}",
    monotonic: true,
    description: "Orders accepted at checkout.",
    declared: true,
    services: ["api"],
    value: (_s, env, at, _q, now) => ORDER_OPS.reduce((a, op) => a + rps("api", env, at, now, op), 0) - errs(env, at, now),
  },
  {
    name: "orders.payment_failed",
    kind: "sum",
    unit: "{order}",
    monotonic: true,
    description: "Orders whose payment could not be taken.",
    declared: true,
    services: ["api"],
    value: (_s, env, at, _q, now) => errs(env, at, now),
  },
  {
    name: "orders.refunds",
    kind: "sum",
    unit: "{order}",
    monotonic: true,
    description: "Refunds issued.",
    declared: true,
    services: ["api"],
    value: (_s, env, at, _q, now) => 0.006 * ORDER_OPS.reduce((a, op) => a + rps("api", env, at, now, op), 0),
  },
  {
    name: "payments.charge.duration",
    kind: "histogram",
    unit: "s",
    description: "Time the payment provider took to answer a charge, retries included.",
    declared: true,
    services: ["api"],
    value: (_s, env, at, q, now) => {
      const s = demoStats({ startMs: at - 2.5 * MINUTE, endMs: at + 2.5 * MINUTE, envIds: [env], service: "api", op: "orders.v1.OrdersService/CreateOrder", now });
      return Math.max(0.02, (quantileOf(q, s) - 20) / 1000);
    },
  },
  {
    name: "rpc.server.duration",
    kind: "histogram",
    unit: "s",
    description: "RPC duration.",
    services: DEMO_BACKENDS,
    value: (service, env, at, q, now) => quantileOf(q, demoStats({ startMs: at - 2.5 * MINUTE, endMs: at + 2.5 * MINUTE, envIds: [env], service, now })) / 1000,
  },
  {
    name: "rpc.server.calls",
    kind: "sum",
    unit: "{call}",
    monotonic: true,
    description: "RPC calls received.",
    services: DEMO_BACKENDS,
    value: (service, env, at, _q, now) => rps(service, env, at, now),
  },
  {
    name: "go.memory.used",
    kind: "sum",
    unit: "By",
    description: "Memory used by the Go runtime.",
    services: DEMO_BACKENDS,
    value: (service, env, at, _q, now) => (BASE_MEMORY[service] ?? 64 * MiB) * (0.85 + 0.15 * load(env, at)) * wiggle(at, service) + (service === "api" && incidentOn(env, at, now) ? 22 * MiB : 0),
  },
  {
    name: "go.memory.allocated",
    kind: "sum",
    unit: "By",
    monotonic: true,
    description: "Memory allocated to the heap.",
    services: DEMO_BACKENDS,
    value: (service, env, at, _q, now) => rps(service, env, at, now) * (service === "api" ? 96 : 48) * 1024,
  },
  {
    name: "go.memory.gc.goal",
    kind: "sum",
    unit: "By",
    description: "Heap size the garbage collector aims for.",
    services: DEMO_BACKENDS,
    value: (service, env, at) => (BASE_MEMORY[service] ?? 64 * MiB) * 1.6 * (0.85 + 0.15 * load(env, at)),
  },
  {
    name: "go.goroutine.count",
    kind: "sum",
    unit: "{goroutine}",
    description: "Live goroutines.",
    services: DEMO_BACKENDS,
    // During the incident, charges waiting on the provider hold goroutines open.
    value: (service, env, at, _q, now) => Math.round((BASE_GOROUTINES[service] ?? 10) * (0.8 + 0.4 * load(env, at)) + (service === "api" && incidentOn(env, at, now) ? 19 : 0)),
  },
  {
    name: "nats.consumer.pending",
    kind: "gauge",
    unit: "{message}",
    description: "Order events waiting for the fulfilment worker.",
    services: ["worker"],
    value: (_s, env, at) => Math.round(2 + 6 * load(env, at) * wiggle(at, "worker")),
  },
  {
    name: "container.cpu.usage",
    kind: "gauge",
    unit: "{cpu}",
    description: "CPU the container used, in cores.",
    services: DEMO_BACKENDS,
    value: (service, env, at) => (BASE_CPU[service] ?? 0.05) * (0.5 + load(env, at)) * wiggle(at, service) * (env === "env-prod" ? 1 : 0.4),
  },
  {
    name: "container.memory.working_set",
    kind: "gauge",
    unit: "By",
    description: "The container's working set memory.",
    services: DEMO_BACKENDS,
    value: (service, env, at) => (BASE_MEMORY[service] ?? 64 * MiB) * 1.22 * (0.9 + 0.1 * load(env, at)) * wiggle(at, service),
  },
];

/** The utilization and restart metrics the workloads dashboard reads (not in the catalog: derived by the platform). */
const DERIVED: Record<string, (service: string, env: DemoEnvKey, at: number) => number> = {
  "k8s.container.cpu_request_utilization": (s, env, at) => CATALOG.find((m) => m.name === "container.cpu.usage")!.value(s, env, at, 0, 0) / (CPU_REQUEST[s] ?? 0.1),
  "k8s.container.cpu_limit_utilization": (s, env, at) => CATALOG.find((m) => m.name === "container.cpu.usage")!.value(s, env, at, 0, 0) / 1,
  "k8s.container.memory_request_utilization": (s, env, at) => CATALOG.find((m) => m.name === "container.memory.working_set")!.value(s, env, at, 0, 0) / MEMORY_REQUEST,
  "k8s.container.memory_limit_utilization": (s, env, at) => CATALOG.find((m) => m.name === "container.memory.working_set")!.value(s, env, at, 0, 0) / MEMORY_LIMIT,
  "k8s.container.restarts": () => 0,
};

function entryOf(spec: MetricSpec, now: number): MetricCatalogEntry {
  return {
    name: spec.name,
    kind: spec.kind,
    unit: spec.unit,
    description: spec.description,
    monotonic: spec.monotonic ?? false,
    temporality: spec.kind === "gauge" ? "unknown" : "cumulative",
    services: [...spec.services],
    serviceCount: spec.services.length,
    lastSeenMs: now - 30_000,
    seriesEstimate: spec.services.length * 3 * (spec.kind === "histogram" ? 7 : 1),
    queryable: true,
    builtin: /^(rpc|go|forge)\./.test(spec.name),
    // forge declares the runtime and RPC metrics it instruments; shop declares its own and the queue's.
    declared: spec.declared ?? !spec.name.startsWith("container."),
    seen: true,
    stability: "stable",
    group: catalogGroup(spec.name, spec.declared ?? false),
  };
}

const envsOf = (envIds: readonly string[] | undefined) => (envIds?.length ? DEMO_ENV_IDS.filter((e) => envIds.includes(e)) : ["env-prod"]) as DemoEnvKey[];

/** One intent's value over services × environments at `at`. */
function valueAt(name: string, intent: SeriesIntent, services: string[], envs: DemoEnvKey[], at: number, stepSeconds: number, now: number): number | null {
  const spec = CATALOG.find((m) => m.name === name);
  const derived = DERIVED[name];
  if (!spec && !derived) return null;
  const q = intent.quantile ?? 0.5;
  const values = envs.flatMap((env) => services.filter((s) => !spec || spec.services.includes(s)).map((s) => (spec ? spec.value(s, env, at, q, now) : derived!(s, env, at))));
  if (!values.length) return null;
  const agg = intent.agg;
  if (spec?.kind === "histogram") return agg === "count" ? values.length * 60 * stepSeconds : Math.max(...values);
  if (spec?.monotonic) {
    const perSecond = values.reduce((a, b) => a + b, 0);
    return agg === "rate" ? perSecond : perSecond * stepSeconds;
  }
  if (agg === "avg") return values.reduce((a, b) => a + b, 0) / values.length;
  if (agg === "max") return Math.max(...values);
  if (agg === "min") return Math.min(...values);
  return values.reduce((a, b) => a + b, 0);
}

const round = (v: number | null) => (v === null ? null : Math.abs(v) >= 100 ? Math.round(v) : Math.round(v * 1000) / 1000);

/**
 * A metrics panel's answer over `window`: one series per intent × group, in
 * the metric's unit; null when the panel names no metric shop emits.
 */
export function shopMetricPanel(panel: Panel, window: TimeWindow, now: number, req?: Pick<QueryPanelRequest, "envIds" | "services" | "groupBy">): PanelResult | null {
  const names = panel.series.map((s) => s.field ?? "");
  if (!names.some((n) => CATALOG.some((m) => m.name === n) || DERIVED[n])) return null;
  const end = Math.min(window.endMs, now);
  const steps = 30;
  const stepSeconds = Math.max(60, Math.round((end - window.startMs) / 1000 / steps));
  const timestamps = Array.from({ length: steps }, (_, i) => Math.floor(window.startMs / 1000) + i * stepSeconds).filter((t) => t * 1000 <= end);
  const envs = envsOf(req?.envIds);
  const services = req?.services?.length ? req.services.filter((s) => (DEMO_BACKENDS as readonly string[]).includes(s)) : [...DEMO_BACKENDS];
  const groupBy = (req?.groupBy?.length ? req.groupBy : (panel.groupBy ?? [])).map((k) => k.replace(/^(resource|metric)\.attr\./, ""));
  type Group = { group: Record<string, string>; services: string[]; from?: number; to?: number };
  let groups: Group[] = [{ group: {}, services }];
  const key = (panel.groupBy ?? req?.groupBy ?? [])[0] ?? "";
  if (groupBy[0] === "k8s.workload.name" || groupBy[0] === "service.name") groups = services.map((s) => ({ group: { [key]: s }, services: [s] }));
  else if (groupBy[0] === "container.image.tag" || groupBy[0] === "service.version") {
    const env = envs[0];
    const ledger = demoLedger(now).filter((p) => p.envId === env && !p.targets.length);
    groups = ledger
      .map((p, i) => ({ group: { [key]: p.version }, services, from: p.atMs, to: ledger[i + 1]?.atMs ?? Infinity }))
      .filter((g) => g.to > window.startMs && g.from < end);
  } else if (groupBy[0] === "k8s.container.last_terminated_reason") groups = [];
  const seriesMeta: SeriesMeta[] = [];
  const series = groups.flatMap((g) =>
    panel.series.map((intent) => {
      const spec = CATALOG.find((m) => m.name === intent.field);
      if (g === groups[0]) {
        seriesMeta.push({
          label: intent.label,
          metric: intent.field ?? "",
          kind: spec?.kind ?? "gauge",
          agg: intent.agg,
          quantile: intent.agg === "quantile" ? intent.quantile : undefined,
          valueUnit: intent.agg === "rate" ? `${spec?.unit ?? ""}/s` : intent.agg === "count" ? "{count}" : (spec?.unit ?? ""),
        });
      }
      return {
        label: intent.label,
        group: g.group,
        timestamps,
        values: timestamps.map((t) => {
          const at = t * 1000 + (stepSeconds * 1000) / 2;
          if (g.from !== undefined && (at < g.from || at >= (g.to ?? Infinity))) return null;
          return round(valueAt(intent.field ?? "", intent, g.services, envs, Math.min(at, now), stepSeconds, now));
        }),
        partial: timestamps.map((t) => (t + stepSeconds) * 1000 > now),
        value: null,
      };
    }),
  );
  // A table, bar or number panel: one value per series, the mean of its points.
  const mean = (values: (number | null)[]) => {
    const xs = values.filter((v): v is number => v !== null);
    return xs.length ? round(xs.reduce((a, b) => a + b, 0) / xs.length) : null;
  };
  if (panel.kind === "table" || panel.kind === "bar") {
    const rows = groups.map((g, gi) => ({ group: g.group, values: panel.series.map((_, si) => mean(series[gi * panel.series.length + si].values)) }));
    return { kind: panel.kind, unit: panel.unit ?? "", series: [], columns: panel.series.map((s) => s.label), rows, stepSeconds: 0, truncated: false, funnelSteps: [], issues: [], sessions: [], seriesMeta, seriesTotal: rows.length, warnings: [], compiledSql: [], window };
  }
  if (panel.kind === "number") {
    return { kind: panel.kind, unit: panel.unit ?? "", series: series.map((x) => ({ ...x, timestamps: [], values: [], partial: [], value: mean(x.values) })), columns: [], rows: [], stepSeconds: 0, truncated: false, funnelSteps: [], issues: [], sessions: [], seriesMeta, seriesTotal: series.length, warnings: [], compiledSql: [], window };
  }
  return {
    kind: panel.kind,
    unit: panel.unit ?? "",
    series,
    columns: [],
    rows: [],
    stepSeconds,
    truncated: false,
    funnelSteps: [],
    issues: [],
    sessions: [],
    seriesMeta,
    seriesTotal: series.length,
    warnings: [],
    compiledSql: [],
    window,
  };
}

export function shopMetrics(now: number): Pick<ObservabilityApi, MetricKeys> {
  return {
    listMetricCatalog: async (req) => {
      const needle = (req.name ?? "").trim().toLowerCase();
      const metrics = CATALOG.map((m) => entryOf(m, now)).filter((m) => !needle || m.name.toLowerCase().includes(needle));
      const truncated = !!req.limit && metrics.length > req.limit;
      return { metrics: req.limit ? metrics.slice(0, req.limit) : metrics, truncated };
    },
    getMetric: async (req): Promise<MetricDetail> => {
      const spec = CATALOG.find((m) => m.name === req.name);
      if (!spec) throw new ConnectError(`metric "${req.name}" has no data in this window`, Code.NotFound);
      const info = entryOf(spec, now);
      const rpc = spec.name.startsWith("rpc.") || spec.name.startsWith("payments.");
      return {
        info,
        intervalSeconds: 60,
        mixedTemporality: false,
        attributes: [
          { key: "service.name", resource: true, distinctEstimate: info.serviceCount, enumerated: [], highCardinality: false },
          { key: "deployment.environment.name", resource: true, distinctEstimate: 3, enumerated: ["dev", "staging", "prod"], highCardinality: false },
          ...(rpc ? [{ key: "rpc.method", resource: false, distinctEstimate: 7, enumerated: [], highCardinality: false }, { key: "status", resource: false, distinctEstimate: 2, enumerated: ["ok", "error"], highCardinality: false }] : []),
        ],
        suggestedBreakdowns: rpc ? ["rpc.method", "status", "service.name"] : ["service.name"],
      };
    },
    listMetricExemplars: async (req) => {
      // A latency histogram's exemplars: the incident's slowest requests, whose traces are stored.
      const failed = failedIncidentRequests(now, Math.max(req.window.startMs, incidentStartMs(now)), Math.min(req.window.endMs, now), req.limit || 20);
      return failed.map(
        (r): MetricExemplar => ({ timeMs: r.startMs, value: DEMO_INCIDENT.deadlineMs / 1000, traceId: r.traceId, spanId: r.chargeSpanId, attributes: { "rpc.method": r.method, status: "error" }, traceStored: true }),
      );
    },
  };
}
