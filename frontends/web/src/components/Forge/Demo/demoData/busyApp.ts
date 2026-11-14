// Copyright (c) 2025 Reliant Labs

/**
 * The demo's busy app: shop's telemetry at the volume a real app has, on top
 * of the console's model fixtures (Observability/__fixtures__), which stay
 * small because component tests pin their rows.
 *
 *   Traces    one request every few seconds across shop's routes and RPCs
 *             (api, orders, inventory, payments, worker): a fast p50, a long
 *             tail and a few slow outliers. CreateOrder / Checkout fail on the
 *             payments call after v1.8.3 reached prod (DEMO_RELEASE_AGO_MS
 *             ago, the fixture ledger's marker). Every fifth request crosses
 *             api → NATS → worker → Connect → inventory → Postgres, with
 *             10–40 spans. The toured trace (FIXTURE_TRACE_ID) is the fixtures'
 *             own, untouched, and always the newest failing CreateOrder.
 *   Logs      several lines per request, linked by trace id, with warn/error
 *             bursts on the payments path that line up with the issue.
 *   Overview  per-service rate with a daily rhythm, and errors and latency
 *             that step up at the v1.8.3 marker.
 *   Profiles  a different CPU profile per service.
 *
 * Deterministic: every value is a function of a request's SLOT (its time
 * bucket since the epoch), so a request keeps its id, route and duration as
 * the window moves, and a page shows the same thing twice. No Math.random.
 * Compact: nothing is stored; a read builds only the slots its window covers,
 * capped (MAX_SLOTS), and pages through them with an opaque cursor.
 */

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { NO_CLIENT } from "@/services/controlPlane/observability/client";
import type { Flamegraph, LogRecord, ServiceHealth, Severity, Span, Trace, TraceSummary } from "@/services/controlPlane/observability/types";

import { createFixtureApi, FIXTURE_TRACE_ID } from "../../Observability/__fixtures__/fixtureApi";
import { FIXTURE_SIGNALS } from "../../Observability/__fixtures__/fixtureSignals";
import { FIXTURE_CLIENTS } from "../../Observability/__fixtures__/fixtureSessions";

/** One request every SLOT_MS. ~1,200 an hour, ~29,000 a day. */
export const SLOT_MS = 3_000;
/** At most this many requests are built for one read (a 24h window is paged from its newest). */
export const MAX_SLOTS = 3_000;
/** When v1.8.3 reached prod: the fixture ledger's marker (fixtureDelivery.ts). */
export const DEMO_RELEASE_AGO_MS = 40 * 60_000;

export const BUSY_SERVICES = ["api", "orders", "inventory", "payments", "worker"] as const;

const ROUTES = [
  { op: "orders.v1.OrdersService/CreateOrder", weight: 14, base: 48, payments: true },
  { op: "orders.v1.OrdersService/GetOrder", weight: 22, base: 9, payments: false },
  { op: "orders.v1.OrdersService/ListOrders", weight: 16, base: 31, payments: false },
  { op: "orders.v1.OrdersService/CancelOrder", weight: 3, base: 22, payments: true },
  { op: "checkout.v1.CheckoutService/Checkout", weight: 8, base: 95, payments: true },
  { op: "inventory.v1.InventoryService/GetStock", weight: 18, base: 6, payments: false },
  { op: "inventory.v1.InventoryService/Reserve", weight: 6, base: 14, payments: false },
  { op: "GET /products/:slug", weight: 10, base: 18, payments: false },
  { op: "worker.fulfilment.tick", weight: 3, base: 210, payments: false },
] as const;
const TOTAL_WEIGHT = ROUTES.reduce((a, r) => a + r.weight, 0);

/** A float in [0, 1) from integers: deterministic, well mixed. */
function hash01(...parts: number[]): number {
  let h = 0x811c9dc5;
  for (const p of parts) {
    h = Math.imul(h ^ (p | 0), 0x01000193);
    h = Math.imul(h ^ ((p / 0x1_0000_0000) | 0), 0x01000193);
  }
  h ^= h >>> 15;
  h = Math.imul(h, 0x2c1b3c6d);
  h ^= h >>> 12;
  return (h >>> 0) / 0x1_0000_0000;
}

const hex = (n: number, len: number) => {
  let s = "";
  for (let i = 0; s.length < len; i++) s += Math.floor(hash01(n, i, 77) * 0x1_0000_0000).toString(16).padStart(8, "0");
  return s.slice(0, len);
};

/** Load over the day: busier in the (UTC) afternoon, quiet at night. */
export function dailyLoad(timeMs: number): number {
  const hour = (timeMs / 3_600_000) % 24;
  return 0.55 + 0.45 * Math.sin(((hour - 9) / 24) * 2 * Math.PI);
}

interface Req {
  slot: number;
  traceId: string;
  startMs: number;
  route: (typeof ROUTES)[number];
  durationMs: number;
  failed: boolean;
  crosses: boolean;
  spanCount: number;
  version: string;
}

/** The request in `slot`, or null when the daily rhythm leaves that slot idle. */
function requestAt(slot: number, releaseAtMs: number): Req | null {
  const startMs = slot * SLOT_MS + Math.floor(hash01(slot, 1) * SLOT_MS);
  if (hash01(slot, 2) > dailyLoad(startMs)) return null;
  let pick = hash01(slot, 3) * TOTAL_WEIGHT;
  const route = ROUTES.find((r) => (pick -= r.weight) < 0) ?? ROUTES[0];
  // Latency: log-normal around the route's base, a long tail, and one in ~150 an outlier.
  const u = Math.max(1e-9, hash01(slot, 4));
  const v = hash01(slot, 5);
  const normal = Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v);
  let durationMs = route.base * Math.exp(0.55 * normal);
  if (hash01(slot, 6) < 1 / 150) durationMs *= 12 + hash01(slot, 7) * 20;
  const afterRelease = startMs >= releaseAtMs;
  // Payments fail rarely before v1.8.3, and often (with a deadline's latency) after it.
  const failRate = route.payments ? (afterRelease ? 0.22 : 0.012) : 0.004;
  const failed = hash01(slot, 8) < failRate;
  if (failed && route.payments) durationMs = Math.max(durationMs, 1_000 + hash01(slot, 9) * 1_500);
  const crosses = route.payments && hash01(slot, 10) < 0.6;
  const spanCount = crosses ? 10 + Math.floor(hash01(slot, 11) * 31) : 2 + Math.floor(hash01(slot, 11) * 6);
  return {
    slot,
    traceId: `${hex(slot, 24)}${(slot >>> 0).toString(16).padStart(8, "0")}`.slice(0, 32),
    startMs,
    route,
    durationMs: Math.round(durationMs * 100) / 100,
    failed,
    crosses,
    spanCount,
    version: afterRelease ? "v1.8.3" : "v1.8.2",
  };
}

/** The requests in [startMs, endMs), newest first, at most MAX_SLOTS slots. */
function requestsIn(startMs: number, endMs: number, releaseAtMs: number, maxSlots = MAX_SLOTS): Req[] {
  const out: Req[] = [];
  const last = Math.floor((endMs - 1) / SLOT_MS);
  const first = Math.max(Math.floor(startMs / SLOT_MS), last - maxSlots + 1);
  for (let slot = last; slot >= first; slot--) {
    const r = requestAt(slot, releaseAtMs);
    if (r && r.startMs >= startMs && r.startMs < endMs) out.push(r);
  }
  return out;
}

const slotOf = (traceId: string): number | null => (/^[0-9a-f]{32}$/.test(traceId) ? parseInt(traceId.slice(24), 16) : null);

function servicesOf(r: Req): string[] {
  const root = r.route.op.split(".")[0];
  const own = root === "checkout" || root === "GET /products/:slug" ? "api" : root === "worker" ? "worker" : root === "orders" ? "orders" : root;
  const set = new Set(["api", own]);
  if (r.route.payments) set.add("payments");
  if (r.crosses) ["worker", "inventory"].forEach((s) => set.add(s));
  return BUSY_SERVICES.filter((s) => set.has(s));
}

function summary(r: Req): TraceSummary {
  const browser = r.route.op.startsWith("GET ") || r.route.op.includes("Checkout");
  const userN = Math.floor(hash01(r.slot, 12) * 400);
  return {
    traceId: r.traceId,
    rootService: browser ? "shop-web" : servicesOf(r)[0],
    rootOperation: r.route.op,
    startMs: r.startMs,
    durationMs: r.durationMs + (r.crosses ? 30 : 0),
    rootDurationMs: r.durationMs,
    spanCount: r.spanCount,
    errorCount: r.failed ? 1 + Math.floor(hash01(r.slot, 13) * 2) : 0,
    services: servicesOf(r),
    userId: browser ? `u_${8000 + userN}` : "",
    sessionId: browser ? `sess-${hex(r.slot, 6)}` : "",
    country: browser ? ["DE", "US", "GB", "FR", "NL"][userN % 5] : "",
    region: "",
    city: "",
    client: browser ? FIXTURE_CLIENTS[userN % FIXTURE_CLIENTS.length] : NO_CLIENT,
    frontendVersion: browser ? r.version : "",
    backendBuilds: servicesOf(r).map((service) => ({ service, version: r.version })),
    source: "",
    operatorData: false,
  };
}

/** A generated request's spans: the root, its queries, and — when it crosses — the queue hop into worker and inventory. */
function traceOf(r: Req): Trace {
  const spans: Span[] = [];
  const span = (id: string, parent: string, name: string, service: string, start: number, duration: number, extra: Partial<Span> = {}): Span => {
    const s: Span = {
      spanId: id,
      parentSpanId: parent,
      name,
      service,
      kind: parent ? "internal" : "server",
      startMs: r.startMs + start,
      durationMs: Math.max(0.05, Math.round(duration * 100) / 100),
      status: "ok",
      statusMessage: "",
      attributes: {},
      resource: { "service.name": service, "service.version": r.version },
      events: [],
      source: "",
      operatorData: false,
      ...extra,
    };
    spans.push(s);
    return s;
  };
  const id = (n: number) => hex(r.slot * 64 + n, 16);
  const root = span(id(0), "", r.route.op, servicesOf(r)[0], 0, r.durationMs, { kind: "server", attributes: { "rpc.system": "connect_rpc" } });
  const queries = Math.max(1, Math.min(r.spanCount - 1, r.crosses ? 3 : r.spanCount - 1));
  let at = 0.6;
  for (let q = 0; q < queries; q++) {
    const d = (r.durationMs * 0.5) / queries;
    span(id(1 + q), root.spanId, ["SELECT orders", "SELECT customers", "INSERT orders", "SELECT stock"][q % 4], root.service, at, d * 0.8, {
      kind: "client",
      attributes: { "db.system.name": "postgresql" },
    });
    at += d;
  }
  if (r.route.payments) {
    const pay = span(id(10), root.spanId, "payments.v1.PaymentsService/Charge", "payments", at, r.durationMs * 0.4, {
      kind: "client",
      ...(r.failed ? { status: "error" as const, statusMessage: "context deadline exceeded" } : {}),
    });
    at += pay.durationMs;
  }
  if (r.crosses) {
    const pub = span(id(11), root.spanId, "nats publish orders.created", root.service, Math.min(at, r.durationMs - 1), 0.6, { kind: "producer" });
    const consume = span(id(12), pub.spanId, "orders.created process", "worker", r.durationMs + 1, 28, { kind: "consumer" });
    const call = span(id(13), consume.spanId, "inventory.v1.InventoryService/Reserve", "worker", r.durationMs + 2, 24, { kind: "client" });
    const server = span(id(14), call.spanId, "inventory.v1.InventoryService/Reserve", "inventory", r.durationMs + 2.5, 22, { kind: "server" });
    // The rest of the span budget: a statement per reserved line item.
    const lines = Math.max(1, r.spanCount - spans.length);
    for (let k = 0; k < lines; k++) {
      span(id(20 + k), server.spanId, "UPDATE stock", "inventory", r.durationMs + 3 + (k * 20) / lines, 20 / lines - 0.1, {
        kind: "client",
        attributes: { "db.system.name": "postgresql", "db.query.text": "UPDATE stock SET reserved = reserved + ? WHERE sku = ? AND available >= ?" },
      });
    }
  }
  return { traceId: r.traceId, spans, truncated: false };
}

const LINES: Record<string, string[]> = {
  ok: ["rpc handled", "order created", "cache hit for customer profile", "stock reserved", "order event published"],
  slow: ["slow query: SELECT * FROM orders WHERE customer_id = $1"],
  warn: ["payments Charge attempt 1 failed, retrying"],
  error: ["payments Charge failed: context deadline exceeded"],
};

/** The lines a request logged, oldest first: several per request, more and louder when it failed. */
function logsOf(r: Req): LogRecord[] {
  const out: LogRecord[] = [];
  const line = (n: number, at: number, severity: Severity, service: string, body: string) =>
    out.push({
      id: `busy:${r.slot}:${n}`,
      timeMs: r.startMs + at,
      severity,
      severityText: severity.toUpperCase(),
      service,
      body,
      traceId: r.traceId,
      spanId: hex(r.slot * 64, 16),
      attributes: { procedure: `/${r.route.op}`, "http.status_code": r.failed ? "503" : "200" },
      resource: { "service.name": service, "k8s.pod.name": `${service}-7d9f8c-x${r.slot % 3}`, "deployment.environment.name": "prod" },
      source: "",
      operatorData: false,
    });
  const service = servicesOf(r)[0];
  line(0, 0.2, "info", service, `${r.route.op.split("/").pop()} started`);
  if (r.durationMs > r.route.base * 3) line(1, r.durationMs * 0.5, "warn", service, LINES.slow[0]);
  if (r.failed && r.route.payments) {
    line(2, r.durationMs * 0.4, "warn", "payments", LINES.warn[0]);
    line(3, r.durationMs * 0.95, "error", "payments", LINES.error[0]);
  } else {
    line(4, r.durationMs * 0.9, hash01(r.slot, 14) < 0.15 ? "debug" : "info", service, LINES.ok[r.slot % LINES.ok.length]);
  }
  if (r.crosses) line(5, r.durationMs + 20, "info", "inventory", `reserved ${1 + (r.slot % 3)} items for order ord_${hex(r.slot, 4)}`);
  return out;
}

/** The services' CPU profiles, one per service: the frames each spends its time in. */
const PROFILES: Record<string, [string[], number][]> = {
  api: [
    [["main", "net/http.(*conn).serve", "orders.(*Service).CreateOrder", "encoding/json.Marshal"], 40],
    [["main", "net/http.(*conn).serve", "orders.(*Service).CreateOrder", "database/sql.(*DB).QueryContext"], 90],
    [["main", "net/http.(*conn).serve", "payments.(*Client).Charge", "net/http.(*Client).Do"], 70],
    [["main", "runtime.gcBgMarkWorker"], 25],
  ],
  inventory: [
    [["main", "connect.(*Handler).ServeHTTP", "inventory.(*Service).Reserve", "pgx.(*Conn).Exec"], 80],
    [["main", "connect.(*Handler).ServeHTTP", "inventory.(*Service).GetStock", "pgx.(*Conn).Query"], 55],
    [["main", "runtime.mallocgc"], 15],
  ],
  worker: [
    [["main", "nats.(*Subscription).processMsg", "fulfilment.(*Worker).Process", "inventory.v1.InventoryServiceClient.Reserve"], 60],
    [["main", "nats.(*Subscription).processMsg", "fulfilment.(*Worker).Process", "encoding/json.Unmarshal"], 30],
    [["main", "lifecyclekit.Every", "fulfilment.(*Worker).Tick"], 20],
  ],
};

function profileOf(service: string): Flamegraph {
  const stacks = PROFILES[service] ?? PROFILES.api;
  const names = [...new Set(stacks.flatMap(([frames]) => frames))];
  const samples = stacks.map(([frames]) => frames.map((f) => names.indexOf(f)));
  const weights = stacks.map(([, w]) => w * 1e6);
  const total = weights.reduce((a, b) => a + b, 0);
  return {
    speedscopeJson: JSON.stringify({
      $schema: "https://www.speedscope.app/file-format-schema.json",
      shared: { frames: names.map((name) => ({ name })) },
      profiles: [{ type: "sampled", name: `${service} cpu`, unit: "nanoseconds", startValue: 0, endValue: total, samples, weights }],
      name: `${service} cpu`,
      exporter: "forge demo",
    }),
    totalValue: total,
    unit: "nanoseconds",
    stackCount: stacks.length,
    availableProfileTypes: ["cpu", "alloc_space", "inuse_space", "goroutine"],
    truncated: false,
  };
}

const offsetOf = (cursor: string | undefined) => (cursor && /^busy:\d+$/.test(cursor) ? Number(cursor.slice(5)) : 0);

/** `base` with shop's traffic at a busy app's volume (see the header). `now` anchors the v1.8.3 marker. */
export function withBusyApp(base: ObservabilityApi, now: number = Date.now()): ObservabilityApi {
  const releaseAtMs = now - DEMO_RELEASE_AGO_MS;
  return {
    ...base,
    listTraces: async (req) => {
      const toured = await base.listTraces(req);
      let all = requestsIn(req.window.startMs, req.window.endMs, releaseAtMs).map(summary);
      // The toured trace is the fixtures' (fixtureTrace): kept, as their newest row.
      const own = toured.traces.find((t) => t.traceId === FIXTURE_TRACE_ID);
      if (own) all = [own, ...all];
      if (req.rootSpanName) all = all.filter((t) => t.rootOperation.includes(req.rootSpanName));
      if (req.services?.length) all = all.filter((t) => t.services.some((s) => req.services.includes(s)));
      if (req.errorsOnly) all = all.filter((t) => t.errorCount > 0);
      if (req.minDurationMs) all = all.filter((t) => t.durationMs >= req.minDurationMs);
      if (req.userId) all = all.filter((t) => t.userId === req.userId);
      if (req.sessionId) all = all.filter((t) => t.sessionId === req.sessionId);
      if (req.browser) all = all.filter((t) => t.client.browserName === req.browser);
      if (req.os) all = all.filter((t) => t.client.osName === req.os);
      if (req.frontendVersion) all = all.filter((t) => t.frontendVersion === req.frontendVersion);
      if (req.backendVersion) all = all.filter((t) => t.backendBuilds.some((b) => b.version === req.backendVersion));
      const durations = all.map((t) => t.durationMs).sort((a, b) => a - b);
      const at = (p: number) => durations[Math.min(durations.length - 1, Math.floor(p * durations.length))] ?? 0;
      const errors = all.filter((t) => t.errorCount > 0).length;
      const red = {
        traceCount: all.length,
        errorTraceCount: errors,
        ratePerSecond: all.length / ((req.window.endMs - req.window.startMs) / 1000),
        errorRate: all.length ? errors / all.length : 0,
        p50Ms: at(0.5),
        p95Ms: at(0.95),
        p99Ms: at(0.99),
      };
      const from = offsetOf(req.cursor);
      const limit = req.limit > 0 ? req.limit : 100;
      const page = all.slice(from, from + limit);
      return { traces: page, nextCursor: from + limit < all.length ? `busy:${from + limit}` : "", red, redGroups: toured.redGroups };
    },
    getTrace: async (req) => {
      const slot = req.traceId === FIXTURE_TRACE_ID ? null : slotOf(req.traceId);
      const r = slot === null ? null : requestAt(slot, releaseAtMs);
      return r && r.traceId === req.traceId ? traceOf(r) : base.getTrace(req);
    },
    searchLogs: async (req) => {
      const toured = await base.searchLogs({ ...req, cursor: undefined });
      const tracedTo = req.traceId;
      let records: LogRecord[];
      if (tracedTo && tracedTo !== FIXTURE_TRACE_ID) {
        const slot = slotOf(tracedTo);
        const r = slot === null ? null : requestAt(slot, releaseAtMs);
        records = r && r.traceId === tracedTo ? logsOf(r).reverse() : [];
      } else {
        const generated = tracedTo ? [] : requestsIn(req.window.startMs, req.window.endMs, releaseAtMs).flatMap((r) => logsOf(r).reverse());
        records = [...toured.records.filter((l) => l.traceId === FIXTURE_TRACE_ID), ...generated].sort((a, b) => b.timeMs - a.timeMs);
      }
      if (req.services?.length) records = records.filter((r) => req.services.includes(r.service));
      if (req.severities?.length) records = records.filter((r) => req.severities.includes(r.severity));
      if (req.query) records = records.filter((r) => r.body.toLowerCase().includes(req.query.toLowerCase()));
      const from = offsetOf(req.cursor);
      const limit = req.limit > 0 ? req.limit : 200;
      return { records: records.slice(from, from + limit), nextCursor: from + limit < records.length ? `busy:${from + limit}` : "" };
    },
    getServiceOverview: async (req) => {
      const res = await base.getServiceOverview(req);
      if ((req.groupBy ?? []).length) return res;
      const names = BUSY_SERVICES.filter((s) => !req.services?.length || req.services.includes(s));
      const steps = 30;
      const stepMs = (req.window.endMs - req.window.startMs) / steps;
      const timestamps = Array.from({ length: steps }, (_, i) => Math.round((req.window.startMs + i * stepMs) / 1000));
      const services: ServiceHealth[] = names.map((name, k) => {
        const scale = [42, 18, 24, 9, 6][k];
        const p50 = [12, 9, 6, 140, 60][k];
        const requestTrend = timestamps.map((t, i) => Math.round(scale * (stepMs / 1000) * dailyLoad(t * 1000) * (0.9 + 0.2 * hash01(k, i, Math.floor(t / 60)))));
        const errorTrend = requestTrend.map((n, i) => {
          const after = timestamps[i] * 1000 >= releaseAtMs;
          const share = name === "payments" ? (after ? 0.09 : 0.004) : name === "api" ? (after ? 0.02 : 0.003) : 0.001;
          return Math.round(n * share * (0.6 + 0.8 * hash01(k, i, 9)));
        });
        const requestCount = requestTrend.reduce((a, b) => a + b, 0);
        const errorCount = errorTrend.reduce((a, b) => a + b, 0);
        const regressed = req.window.endMs > releaseAtMs && (name === "payments" || name === "api");
        const p95 = p50 * (regressed ? 6.5 : 3.2);
        return {
          name,
          group: {},
          requestCount,
          requestRate: requestCount / ((req.window.endMs - req.window.startMs) / 1000),
          errorCount,
          errorRate: requestCount ? errorCount / requestCount : 0,
          p50Ms: p50,
          p95Ms: p95,
          p99Ms: p95 * 2.2,
          errorLogCount: Math.round(errorCount * 1.4),
          timestamps,
          requestTrend,
          errorTrend,
          rateSeries: requestTrend.map((n) => n / (stepMs / 1000)),
          errorRateSeries: requestTrend.map((n, i) => (n ? errorTrend[i] / n : null)),
          source: "",
          operatorData: false,
        };
      });
      return { services, trendStepSeconds: Math.round(stepMs / 1000) };
    },
    getFlamegraph: async (req) => (req.traceId === FIXTURE_TRACE_ID ? base.getFlamegraph(req) : profileOf(req.service || "api")),
  };
}

/**
 * The demo's data, in every shell and on the wire alike: the sample app's
 * fixtures (the shop graph, the toured trace crossing into inventory, more
 * issues, dashboards read-only) with the busy app's traffic on top.
 */
export function createDemoFixtures(now: number = Date.now()): ObservabilityApi {
  return withBusyApp(createFixtureApi({ signals: FIXTURE_SIGNALS, dashboardsReadOnly: true, graph: "shop", crossService: true, moreIssues: true }), now);
}

/** Test seams. */
export const busyAppInternals = { requestAt, requestsIn, traceOf, logsOf, summary };
