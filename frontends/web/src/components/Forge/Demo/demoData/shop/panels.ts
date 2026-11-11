// Copyright (c) 2025 Reliant Labs

/**
 * QueryPanel, attribute values and usage over shop's story: a dashboard
 * panel's numbers come from the same traffic model (demoStats) the overview,
 * traces and service map read, so a dashboard agrees with every other page.
 *
 * A traces panel is read as the request scope its predicates name (a span
 * name, a prefix, a service, errors only, the payment charge) and answered
 * per its kind (number, line, table/bar, heatmap) and group-by (service,
 * span name, release version, …). Metrics panels go to metrics.ts, Postgres
 * panels to the `shop` database (database.ts), and the browser kinds
 * (funnel, sessions, issues) to the storefront's traffic and sessions.
 */

import type { DatabasePanelResult, QueryDatabasePanelInput } from "@/services/controlPlane/observability/appDatabaseApi";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { ObservabilityUsage, Panel, PanelResult, Predicate, QueryPanelRequest, SeriesIntent, TimeWindow } from "@/services/controlPlane/observability/types";

import {
  DEMO_BACKENDS,
  DEMO_ENV_IDS,
  DEMO_FRONTEND,
  DEMO_INCIDENT,
  DEMO_ROUTES,
  DEMO_RPC_METHODS,
  demoLedger,
  demoStats,
  type DemoEnvKey,
  type DemoStats,
  type DemoStatsQuery,
} from "../shopStory";
import { shopDatabase } from "./database";
import { shopMetricPanel } from "./metrics";
import { productAnalyticsPanel, shopCommerce } from "./productAnalytics";
import type { PanelKeys } from "./shopApi";

const BLANK: Omit<PanelResult, "kind" | "unit"> = {
  series: [],
  columns: [],
  rows: [],
  stepSeconds: 0,
  truncated: false,
  funnelSteps: [],
  issues: [],
  sessions: [],
  seriesMeta: [],
  seriesTotal: 0,
  warnings: [],
  compiledSql: [],
};

const ENV_NAME: Record<string, DemoEnvKey> = { dev: "env-dev", staging: "env-staging", prod: "env-prod" };

/** The request scope a panel's predicates (and the dashboard's filters) name. */
interface Scope {
  envIds: DemoEnvKey[];
  services: string[];
  ops: string[] | null;
  errorsOnly: boolean;
  /** The payment charge's client span (the incident's span), not a server span. */
  charge: boolean;
  /** A database client span. */
  database: boolean;
  /** The logs signal: counts of log lines. */
  logs: boolean;
}

const ALL_OPS = [...DEMO_ROUTES.map((r) => r.op as string), ...DEMO_ROUTES.flatMap((r) => r.hops.map((h) => h.op as string))];

function matches(p: Predicate, value: string): boolean {
  const v = p.value ?? "";
  switch (p.op) {
    case "eq":
      return value === v;
    case "neq":
      return value !== v;
    case "prefix":
      return value.startsWith(v);
    case "match":
      try {
        return new RegExp(v).test(value);
      } catch {
        return true;
      }
    case "in":
      return (p.values ?? []).includes(value);
    case "contains":
      return value.includes(v);
    default:
      return true;
  }
}

function scopeOf(where: Predicate[], req: Pick<QueryPanelRequest, "envIds" | "services" | "filters">, signal: string): Scope {
  const preds = [...where, ...(req.filters ?? []).map((f) => ({ field: f.key, op: f.op, value: f.value, values: f.values }))];
  let envIds = (req.envIds.length ? DEMO_ENV_IDS.filter((e) => req.envIds.includes(e)) : [...DEMO_ENV_IDS]) as DemoEnvKey[];
  let services = req.services?.length ? [...req.services] : [];
  let ops: string[] | null = null;
  let errorsOnly = false;
  let charge = false;
  let database = false;
  for (const p of preds) {
    const field = p.field.replace(/^(resource|span|log)\.attr\./, "");
    if (field === "deployment.environment.name") envIds = envIds.filter((e) => matches(p, Object.keys(ENV_NAME).find((n) => ENV_NAME[n] === e)!));
    else if (field === "service.name") services = (services.length ? services : [...DEMO_BACKENDS, DEMO_FRONTEND]).filter((s) => matches(p, s));
    else if (field === "span.status" || field === "log.severity") errorsOnly = errorsOnly || p.value === "error";
    else if (field === "span.name") {
      if (p.value === "payments.Charge") charge = true;
      else ops = (ops ?? ALL_OPS).filter((o) => matches(p, o));
    } else if (field === "db.system" || field === "db.system.name" || field === "db.operation") database = true;
    else if (field === "procedure") ops = (ops ?? ALL_OPS).filter((o) => matches(p, `/${o}`));
  }
  return { envIds, services, ops, errorsOnly, charge, database, logs: signal === "logs" };
}

const CHARGING = DEMO_ROUTES.filter((r) => r.charges).map((r) => r.op as string);

/** RED for a scope over a window: sums over its services × ops. */
function statsOf(scope: Scope, window: TimeWindow, now: number): DemoStats {
  const end = Math.min(window.endMs, now);
  const base: DemoStatsQuery = { startMs: window.startMs, endMs: Math.max(window.startMs + 1, end), envIds: scope.envIds, now };
  const queries: DemoStatsQuery[] = [];
  const ops = scope.charge ? CHARGING : scope.ops;
  const services = scope.services.filter((s) => s !== DEMO_FRONTEND);
  if (services.length) for (const service of services) for (const op of ops ?? [undefined]) queries.push({ ...base, service, op });
  else for (const op of ops ?? [undefined]) queries.push({ ...base, op });
  const all = queries.map(demoStats);
  const requests = all.reduce((a, s) => a + s.requests, 0);
  const errors = all.reduce((a, s) => a + s.errors, 0);
  const heaviest = all.reduce((a, s) => (s.requests > a.requests ? s : a), all[0] ?? { requests: 0, errors: 0, errorRate: 0, ratePerSecond: 0, p50Ms: 0, p95Ms: 0, p99Ms: 0 });
  // Latency of a mixed scope: the weighted mean of its parts' quantiles (close enough for a panel).
  const weighted = (k: "p50Ms" | "p95Ms" | "p99Ms") => (requests ? all.reduce((a, s) => a + s[k] * s.requests, 0) / requests : heaviest[k]);
  const dbScale = scope.database ? 0.12 : 1;
  const mult = scope.logs ? 2.6 : scope.database ? 2.2 : 1;
  return {
    requests: Math.round(requests * mult),
    errors: Math.round(errors * (scope.logs ? 1 : mult)),
    errorRate: requests ? errors / requests : 0,
    ratePerSecond: (requests * mult) / Math.max(1, (base.endMs - base.startMs) / 1000),
    p50Ms: Math.round(weighted("p50Ms") * dbScale * 10) / 10,
    p95Ms: Math.round(weighted("p95Ms") * (scope.database ? 0.04 : 1) * 10) / 10,
    p99Ms: Math.round(weighted("p99Ms") * (scope.database ? 0.03 : 1) * 10) / 10,
  };
}

/** One intent's value from a scope's stats. */
function valueOf(intent: SeriesIntent, stats: DemoStats, scope: Scope): number {
  const errorIntent = scope.errorsOnly || (intent.where ?? []).some((p) => p.value === "error");
  if (intent.agg === "quantile") {
    const q = intent.quantile ?? 0.5;
    return q >= 0.99 ? stats.p99Ms : q >= 0.95 ? stats.p95Ms : q >= 0.9 ? (stats.p50Ms + stats.p95Ms) / 2 : stats.p50Ms;
  }
  if (intent.agg === "count_distinct") return Math.round(stats.requests * 0.18);
  return errorIntent ? stats.errors : stats.requests;
}

/** A group-by key's groups for a scope: each with the scope narrowed to it. */
function groupsOf(keys: string[], scope: Scope, window: TimeWindow, now: number): { group: Record<string, string>; scope: Scope; window?: TimeWindow }[] {
  if (!keys.length) return [{ group: {}, scope }];
  const [key, ...rest] = keys;
  const field = key.replace(/^(resource|span|metric|log)\.attr\./, "");
  const nest = (group: Record<string, string>, narrowed: Scope, w?: TimeWindow) => groupsOf(rest, narrowed, w ?? window, now).map((g) => ({ group: { [key]: group[key], ...g.group }, scope: g.scope, window: g.window ?? w }));
  switch (field) {
    case "service.name": {
      const services = scope.services.length ? scope.services.filter((s) => s !== DEMO_FRONTEND) : [...DEMO_BACKENDS];
      return services.flatMap((s) => nest({ [key]: s }, { ...scope, services: [s] }));
    }
    case "deployment.environment.name":
      return scope.envIds.flatMap((e) => nest({ [key]: Object.keys(ENV_NAME).find((n) => ENV_NAME[n] === e)! }, { ...scope, envIds: [e] }));
    case "span.name":
    case "rpc.method": {
      const ops = (scope.ops ?? DEMO_RPC_METHODS.map((m) => m.id)).filter((o) => DEMO_RPC_METHODS.some((m) => m.id === o) && (!scope.services.length || scope.services.includes(DEMO_RPC_METHODS.find((m) => m.id === o)!.process)));
      return ops.flatMap((o) => nest({ [key]: field === "rpc.method" ? o.split("/")[1] : o }, { ...scope, ops: [o] }));
    }
    case "service.version":
    case "container.image.tag": {
      // The windows each release ran in, in the scope's (first) environment.
      const env = scope.envIds.includes("env-prod") ? "env-prod" : scope.envIds[0];
      const ledger = demoLedger(now).filter((p) => p.envId === env && !p.targets.length);
      const out: { group: Record<string, string>; scope: Scope; window?: TimeWindow }[] = [];
      ledger.forEach((p, i) => {
        const from = Math.max(window.startMs, p.atMs);
        const to = Math.min(window.endMs, ledger[i + 1]?.atMs ?? window.endMs);
        if (to > from) out.push(...nest({ [key]: p.version }, { ...scope, envIds: [env] }, { startMs: from, endMs: to }));
      });
      return out;
    }
    case "k8s.workload.name":
      return [...DEMO_BACKENDS].flatMap((s) => nest({ [key]: s }, { ...scope, services: [s] }));
    case "db.operation":
      return ["SELECT", "INSERT", "UPDATE"].flatMap((op, i) => nest({ [key]: op }, { ...scope, database: true }, undefined).map((g) => ({ ...g, share: [0.7, 0.18, 0.12][i] })));
    case "span.status_message":
      return [{ group: { [key]: DEMO_INCIDENT.error }, scope: { ...scope, ops: CHARGING, errorsOnly: true } }];
    default:
      return [];
  }
}

function tracesPanel(panel: Panel, req: QueryPanelRequest, now: number): PanelResult {
  const window = req.window;
  const scope = scopeOf(panel.where ?? [], req, panel.signal);
  const groupBy = req.groupBy?.length ? req.groupBy : (panel.groupBy ?? []);
  // A top-errors table grouped by span name and message: the incident's operations, one row each.
  const groups =
    groupBy.length === 2 && groupBy.some((k) => k.endsWith("status_message"))
      ? CHARGING.map((op) => ({ group: { [groupBy[0]]: op, [groupBy[1]]: DEMO_INCIDENT.error }, scope: { ...scope, ops: [op], services: ["api"], errorsOnly: true }, window: undefined as TimeWindow | undefined }))
      : groupsOf(groupBy, scope, window, now);
  const result = { kind: panel.kind, unit: panel.unit ?? "", ...BLANK, window };
  const share = (g: unknown) => (g as { share?: number }).share ?? 1;
  if (panel.kind === "table" || panel.kind === "bar") {
    const rows = groups
      .map((g) => {
        const stats = statsOf(g.scope, g.window ?? window, now);
        return { group: g.group, values: panel.series.map((intent) => Math.round(valueOf(intent, stats, g.scope) * (intent.agg === "quantile" ? 1 : share(g)) * 10) / 10) };
      })
      .filter((r) => !scope.errorsOnly || r.values.some((v) => v > 0));
    rows.sort((a, b) => (b.values[0] ?? 0) - (a.values[0] ?? 0));
    return { ...result, columns: panel.series.map((s) => s.label), rows };
  }
  if (panel.kind === "number") {
    const intents = panel.ratio ? [{ label: "ratio", agg: "ratio" }] : panel.series;
    return {
      ...result,
      series: groups.flatMap((g) => {
        const stats = statsOf(g.scope, g.window ?? window, now);
        return intents.map((intent) => ({ label: intent.label, group: g.group, timestamps: [], values: [], value: panel.ratio ? stats.errorRate : valueOf(intent, stats, g.scope) * (intent.agg === "quantile" ? 1 : share(g)) }));
      }),
    };
  }
  if (panel.kind === "heatmap") {
    const steps = 30;
    const stepMs = (window.endMs - window.startMs) / steps;
    const bounds = [0, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000];
    const heatmap = Array.from({ length: steps }, (_, t) => {
      const from = window.startMs + t * stepMs;
      const stats = statsOf(scope, { startMs: from, endMs: from + stepMs }, now);
      return bounds.slice(0, -1).map((low, b) => {
        const high = bounds[b + 1];
        // Share of requests in [low, high): a log-normal around the median, plus the slow tail the p95/p99 say is there.
        const z = (x: number) => (x <= 0 ? 0 : 1 / (1 + Math.exp(-2.2 * Math.log(x / Math.max(1, stats.p50Ms)))));
        const tail = (x: number) => (x <= 0 ? 0 : 1 / (1 + Math.exp(-6 * Math.log(x / Math.max(1, stats.p99Ms)))));
        const cdf = (x: number) => 0.95 * z(x) + 0.05 * tail(x);
        return { timeSec: Math.round(from / 1000), low, high, count: Math.round(stats.requests * Math.max(0, cdf(high) - cdf(low))) };
      });
    }).flat();
    return { ...result, stepSeconds: Math.round(stepMs / 1000), heatmap };
  }
  // A line: one series per intent × group, over 30 buckets (each group in its own window, if it has one).
  const steps = 30;
  const stepMs = (window.endMs - window.startMs) / steps;
  const timestamps = Array.from({ length: steps }, (_, i) => Math.round((window.startMs + i * stepMs) / 1000));
  const intents: SeriesIntent[] = panel.ratio ? [{ label: "ratio", agg: "ratio" }] : panel.series;
  return {
    ...result,
    stepSeconds: Math.round(stepMs / 1000),
    series: groups.flatMap((g) =>
      intents.map((intent) => ({
        label: intent.label,
        group: g.group,
        timestamps,
        values: timestamps.map((t) => {
          const from = t * 1000;
          const to = from + stepMs;
          const w = g.window ?? window;
          if (to <= w.startMs || from >= w.endMs || from > now) return null;
          const stats = statsOf(g.scope, { startMs: Math.max(from, w.startMs), endMs: Math.min(to, w.endMs) }, now);
          if (panel.ratio) return stats.requests ? stats.errors / Math.max(1, stats.requests) : null;
          return Math.round(valueOf(intent, stats, g.scope) * (intent.agg === "quantile" ? 1 : share(g)) * 10) / 10;
        }),
        value: null,
      })),
    ),
  };
}

/**
 * The storefront's funnel over a window, per session: product views, carts,
 * checkouts begun and purchases. Read from the same audience as the Product
 * dashboard's Buy funnel (productAnalytics.ts), so the two agree: the last
 * step is the purchases, which since v1.8.3 fall short of the orders by the
 * charges the incident failed.
 */
function funnelPanel(panel: Panel, req: QueryPanelRequest, now: number): PanelResult {
  const window = { startMs: req.window.startMs, endMs: Math.min(req.window.endMs, now) };
  const envIds = (req.envIds.length ? DEMO_ENV_IDS.filter((e) => req.envIds.includes(e)) : [...DEMO_ENV_IDS]) as DemoEnvKey[];
  const c = shopCommerce(envIds, window.startMs, window.endMs, now);
  // A person views products in ≈1.3 sessions and returns to a cart in ≈1.1; a checkout and a purchase are one session each.
  const counts = [Math.round(c.viewers * 1.3), Math.round(c.cart * 1.1), c.checkout, c.purchases];
  const steps = panel.funnel?.steps?.length ? panel.funnel.steps : ["view_product", "add_to_cart", "begin_checkout", "purchase"];
  return {
    kind: panel.kind,
    unit: "",
    ...BLANK,
    window,
    funnelSteps: steps.map((event, i) => ({ event, count: counts[i] ?? counts[counts.length - 1], rateFromFirst: counts[0] ? (counts[i] ?? 0) / counts[0] : 0, rateFromPrevious: i === 0 ? 1 : counts[i - 1] ? (counts[i] ?? 0) / counts[i - 1] : 0 })),
  };
}

/** A Postgres panel over the `shop` database (dashboards.ts), as control-plane answers it. */
export function shopDatabasePanel(req: QueryDatabasePanelInput, now: number): DatabasePanelResult {
  const base = { kind: "postgres", unit: "", ...BLANK, truncatedBy: "", window: req.window, database: req.query.database || "shop", replica: "ro", servedFrom: "db", durationMs: 9 };
  const orders = shopDatabase(req.environmentId || "env-prod", now).orders.filter((o) => {
    const t = Date.parse(String(o.created_at).replace(" ", "T").replace("+00", "Z"));
    return t >= req.window.startMs && t < req.window.endMs;
  });
  if (req.query.draw === "line" || req.query.time) {
    const step = Math.max(3600, Math.round((req.window.endMs - req.window.startMs) / 1000 / 24 / 3600) * 3600);
    const first = Math.floor(req.window.startMs / 1000 / step) * step;
    const timestamps: number[] = [];
    for (let t = first; t * 1000 < req.window.endMs; t += step) timestamps.push(t);
    const values = timestamps.map((t) => orders.filter((o) => {
      const at = Date.parse(String(o.created_at).replace(" ", "T").replace("+00", "Z")) / 1000;
      return at >= t && at < t + step;
    }).length);
    return { ...base, stepSeconds: step, series: [{ label: req.query.values?.[0] ?? "orders", group: {}, timestamps, values, value: null }] };
  }
  const byState = new Map<string, number>();
  for (const o of orders) byState.set(String(o.state), (byState.get(String(o.state)) ?? 0) + 1);
  return { ...base, columns: ["orders"], rows: [...byState.entries()].sort((a, b) => b[1] - a[1]).map(([state, n]) => ({ group: { state }, values: [n] })) };
}

/** Values a key takes in shop, most frequent first. */
function attributeValues(key: string, envIds: readonly string[], now: number): string[] {
  const field = key.replace(/^(resource|span|log|metric)\.attr\./, "");
  const envs = envIds.length ? DEMO_ENV_IDS.filter((e) => envIds.includes(e)) : [...DEMO_ENV_IDS];
  switch (field) {
    case "deployment.environment.name":
      return envs.map((e) => Object.keys(ENV_NAME).find((n) => ENV_NAME[n] === e)!);
    case "reliant.env.id":
      return envs;
    case "service.name":
      return [...DEMO_BACKENDS, DEMO_FRONTEND];
    case "k8s.workload.name":
      return [...DEMO_BACKENDS];
    case "service.version":
    case "container.image.tag":
      return [...new Set(demoLedger(now).filter((p) => envs.includes(p.envId)).map((p) => p.version))].reverse();
    case "status":
    case "span.status":
      return ["ok", "error"];
    case "rpc.method":
      return DEMO_RPC_METHODS.map((m) => m.method);
    case "rpc.service":
      return [...new Set(DEMO_RPC_METHODS.map((m) => m.service))];
    case "span.name":
      return DEMO_ROUTES.map((r) => r.op);
    case "log.severity":
      return ["info", "debug", "warn", "error"];
    case "k8s.event.reason":
      return ["Pulled", "Scheduled", "Started", "Created"];
    default:
      return [];
  }
}

const GIB = 2 ** 30;

/** shop's org this month: comfortably inside the allowance. */
function usage(now: number): ObservabilityUsage {
  const d = new Date(now);
  const start = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1);
  const end = Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1);
  const elapsed = (now - start) / (end - start);
  const used = 6.4 * GIB * Math.max(0.2, elapsed);
  return {
    periodStartMs: start,
    periodEndMs: end,
    measuredThroughMs: now - 10 * 60_000,
    usedBytes: used,
    allowanceBytes: 10 * GIB,
    overageCents: 0,
    projectedBytes: used / Math.max(0.05, elapsed),
    projectedOverageCents: 0,
    rateCentsPerGb: "25.0000",
    state: "ok",
    usageState: "ok",
    funded: false,
    exempt: false,
    spendCapCents: 5000,
    spendCapReached: false,
    browserBytes: 0.08 * used,
    browserBilledBytes: 0.08 * used,
    browserCapBytes: 2 * GIB,
    metadataBytes: 0.05 * used,
    bySignal: [
      { signal: "logs", billedBytes: 0.48 * used, storedBytes: 0.48 * used, rows: Math.round(used / 340) },
      { signal: "traces", billedBytes: 0.3 * used, storedBytes: 0.3 * used, rows: Math.round(used / 760) },
      { signal: "metrics", billedBytes: 0.14 * used, storedBytes: 0.14 * used, rows: Math.round(used / 52) },
      { signal: "profiles", billedBytes: 0.08 * used, storedBytes: 0.08 * used, rows: Math.round(used / 9000) },
    ],
    byService: [
      { service: "api", signal: "logs", billedBytes: 0.3 * used },
      { service: "api", signal: "traces", billedBytes: 0.19 * used },
      { service: "inventory", signal: "logs", billedBytes: 0.1 * used },
      { service: "worker", signal: "logs", billedBytes: 0.08 * used },
      { service: "inventory", signal: "traces", billedBytes: 0.07 * used },
      { service: "worker", signal: "traces", billedBytes: 0.04 * used },
    ],
  };
}

export function shopPanels(now: number): Pick<ObservabilityApi, PanelKeys> {
  return {
    queryPanel: async (req) => {
      const { panel } = req;
      if (req.explain) return { kind: panel.kind, unit: panel.unit ?? "", ...BLANK, window: req.window };
      if (panel.signal === "metrics") return shopMetricPanel(panel, req.window, now, req) ?? { kind: panel.kind, unit: panel.unit ?? "", ...BLANK, window: req.window };
      if (panel.kind === "funnel") return funnelPanel(panel, req, now);
      // The built-in product analytics dashboards' sql panels, by panel id.
      const analytics = productAnalyticsPanel(req, now);
      if (analytics) return analytics;
      if (panel.kind === "issues" || panel.kind === "sessions" || panel.kind === "search" || panel.kind === "sql" || panel.kind === "text") {
        // The browser kinds are read from issues/sessions by the views; search from SearchLogs.
        return { kind: panel.kind, unit: panel.unit ?? "", ...BLANK, window: req.window };
      }
      return tracesPanel(panel, req, now);
    },
    listAttributeValues: async (req) => {
      const values = attributeValues(req.key, req.envIds, now)
        .filter((v) => v.toLowerCase().startsWith((req.prefix ?? "").toLowerCase()))
        .map((value, i) => ({ value, count: Math.round(42_000 / (i + 1)) }));
      return { values: req.limit ? values.slice(0, req.limit) : values, truncated: false };
    },
    getObservabilityUsage: async () => usage(now),
  };
}
