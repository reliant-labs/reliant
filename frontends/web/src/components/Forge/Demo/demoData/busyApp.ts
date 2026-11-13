// Copyright (c) 2025 Reliant Labs

/**
 * shop's request-level telemetry: every trace, span, log line and profile
 * the demo shows, sampled from the story's traffic model (shopStory.ts) so
 * that what a list shows and what an aggregate says agree.
 *
 *   Requests  one potential request per SLOT_MS per environment, kept with
 *             the story's load (dailyLoad × ENV_TRAFFIC): ~2,400 an hour in
 *             prod at peak, a trickle in staging and dev. Each picks a route
 *             (DEMO_ROUTES) by weight and a log-normal latency around its
 *             base, with a rare outlier.
 *   Incident  in prod after v1.8.3's promotion, a charging route (CreateOrder,
 *             Checkout) fails DEMO_INCIDENT.failShare of the time at its 3s
 *             deadline, and is slow (retried, then succeeded) slowShare of
 *             the time. Nowhere else does anything fail.
 *   Traces    the root span, its queries, the charge to the payment provider
 *             (two attempts when it retried), and — on six in ten orders — the
 *             order event's hop api → NATS → worker → inventory (10–40 spans).
 *             A browser-started request (Checkout, a product page) has
 *             shop-web's span as its root. The toured trace (DEMO_TOURED_TRACE)
 *             is prod's newest failing CreateOrder, with the span ids the tour
 *             points at.
 *   Logs      a few lines per request, linked by trace and span, each saying
 *             what that request did.
 *   Profiles  per service and per profile type, in the type's unit.
 *
 * Deterministic: a request is a function of its environment and SLOT (its
 * time bucket since the epoch), so it keeps its id, route and duration as
 * the window moves. No Math.random. Compact: nothing is stored; a read builds
 * only the slots its window covers (capped, MAX_SLOTS) and pages through them
 * with an opaque cursor.
 */

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { NO_CLIENT } from "@/services/controlPlane/observability/client";
import type { Flamegraph, LogRecord, ServiceHealth, ServiceSignalsResult, Severity, Span, Trace, TraceRed, TraceSummary } from "@/services/controlPlane/observability/types";

import { DEMO_CLIENTS } from "./shop/clients";
import {
  DEMO_BACKENDS,
  DEMO_CUSTOMERS,
  DEMO_DEPENDENCIES,
  DEMO_ENV_IDS,
  DEMO_FRONTEND,
  DEMO_INCIDENT,
  DEMO_PAYMENTS_HOST,
  DEMO_PRODUCTS,
  DEMO_ROUTE_WEIGHT,
  DEMO_ROUTES,
  DEMO_TOURED_TRACE,
  dailyLoad,
  demoLedger,
  demoNow,
  demoStats,
  demoTrend,
  ENV_TRAFFIC,
  incidentStartMs,
  LATENCY_SIGMA,
  OUTLIER_FACTOR,
  OUTLIER_SHARE,
  RUNNING,
  SLOT_MS,
  type DemoEnvKey,
  type DemoRoute,
} from "./shopStory";

export { dailyLoad, SLOT_MS };

/** At most this many slots per environment are built for one read (a 24h window is paged from its newest). */
export const MAX_SLOTS = 3_000;
/** When v1.8.3 reached prod (RUNNING's age): the incident's start. */
export const DEMO_RELEASE_AGO_MS = RUNNING["env-prod"].age;

/** The services that emit server telemetry (traces' services also include shop-web for browser-started requests). */
export const BUSY_SERVICES = DEMO_BACKENDS;

const ENV_NAME: Record<DemoEnvKey, string> = { "env-dev": "dev", "env-staging": "staging", "env-prod": "prod" };

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

const hex = (n: number, len: number, salt = 77) => {
  let s = "";
  for (let i = 0; s.length < len; i++) s += Math.floor(hash01(n, i, salt) * 0x1_0000_0000).toString(16).padStart(8, "0");
  return s.slice(0, len);
};

/** What a read needs to know about the world: now, and which release each environment ran when. */
interface Ctx {
  now: number;
  /** Per environment: [promotedAtMs, version], oldest first (whole-environment promotions). */
  versions: Record<DemoEnvKey, [number, string][]>;
  /** The toured trace's start. No generated prod CreateOrder fails after it. */
  touredAtMs: number;
}

/**
 * How long before "now" the toured trace started. The tour tells the visitor to
 * open it "at the top" of the traces view, which lists the newest first and
 * renders only the rows on screen; every request generated after it sits above
 * it. Ten seconds keeps it in the first ten rows at shop's busiest hour (a
 * minute put it 13th to 46th by the time of day, off screen for most of the day).
 */
export const TOURED_AGO_MS = 10_000;

function ctxOf(now: number): Ctx {
  const versions = Object.fromEntries(DEMO_ENV_IDS.map((e) => [e, [] as [number, string][]])) as Ctx["versions"];
  for (const p of demoLedger(now)) if (!p.targets.length) versions[p.envId].push([p.atMs, p.version]);
  return { now, versions, touredAtMs: now - TOURED_AGO_MS };
}

function versionAt(ctx: Ctx, env: DemoEnvKey, atMs: number): string {
  let v = ctx.versions[env][0]?.[1] ?? "v1.8.1";
  for (const [at, version] of ctx.versions[env]) if (at <= atMs) v = version;
  return v;
}

interface Req {
  env: DemoEnvKey;
  envIndex: number;
  slot: number;
  traceId: string;
  startMs: number;
  route: DemoRoute;
  durationMs: number;
  /** The charge timed out twice: the request failed at its deadline. */
  failed: boolean;
  /** The charge timed out once, then succeeded on the retry. */
  retried: boolean;
  crosses: boolean;
  spanCount: number;
  version: string;
  customer: (typeof DEMO_CUSTOMERS)[number];
  order: string;
}

/** The request in `env`'s `slot`, or null when the load leaves that slot idle. */
function requestAt(ctx: Ctx, env: DemoEnvKey, slot: number): Req | null {
  const e = DEMO_ENV_IDS.indexOf(env);
  const startMs = slot * SLOT_MS + Math.floor(hash01(slot, 1, e) * SLOT_MS);
  if (startMs > ctx.now) return null;
  if (hash01(slot, 2, e) >= dailyLoad(startMs) * ENV_TRAFFIC[env]) return null;
  let pick = hash01(slot, 3, e) * DEMO_ROUTE_WEIGHT;
  const route = DEMO_ROUTES.find((r) => (pick -= r.weight) < 0) ?? DEMO_ROUTES[0];
  // Latency: log-normal around the route's base, and one in ~150 an outlier.
  const u = Math.max(1e-9, hash01(slot, 4, e));
  const v = hash01(slot, 5, e);
  const normal = Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v);
  let durationMs = hash01(slot, 6, e) < OUTLIER_SHARE ? route.base * OUTLIER_FACTOR * Math.exp(0.3 * normal) : route.base * Math.exp(LATENCY_SIGMA * normal);
  const incident = route.charges && env === "env-prod" && startMs >= incidentStartMs(ctx.now);
  const roll = hash01(slot, 8, e);
  let failed = incident && roll < DEMO_INCIDENT.failShare;
  // The toured trace is prod's newest failing CreateOrder.
  if (failed && route.op.endsWith("/CreateOrder") && startMs >= ctx.touredAtMs) failed = false;
  const retried = incident && !failed && roll >= DEMO_INCIDENT.failShare && roll < DEMO_INCIDENT.failShare + DEMO_INCIDENT.slowShare;
  if (failed) durationMs = DEMO_INCIDENT.deadlineMs + 10 + hash01(slot, 9, e) * 20;
  else if (retried) durationMs = route.base + DEMO_INCIDENT.retryMs + 120 * Math.exp(0.08 * normal);
  const crosses = route.hops.length > 0 && hash01(slot, 10, e) < 0.6;
  const spanCount = crosses ? 10 + Math.floor(hash01(slot, 11, e) * 31) : 3 + Math.floor(hash01(slot, 11, e) * 4);
  return {
    env,
    envIndex: e,
    slot,
    traceId: `${hex(slot * 3 + e, 23)}${e.toString(16)}${(slot >>> 0).toString(16).padStart(8, "0")}`,
    startMs,
    route,
    durationMs: Math.round(durationMs * 100) / 100,
    failed,
    retried,
    crosses,
    spanCount,
    version: versionAt(ctx, env, startMs),
    customer: DEMO_CUSTOMERS[Math.floor(hash01(slot, 12, e) * DEMO_CUSTOMERS.length)],
    order: `ord_${hex(slot * 3 + e, 6, 31)}`,
  };
}

/** The requests in [startMs, endMs) of `envs`, newest first, at most MAX_SLOTS slots each. */
function requestsIn(ctx: Ctx, startMs: number, endMs: number, envs: readonly DemoEnvKey[] = DEMO_ENV_IDS, maxSlots = MAX_SLOTS): { requests: Req[]; sampledFromMs: number } {
  const out: Req[] = [];
  const last = Math.floor((Math.min(endMs, ctx.now + 1) - 1) / SLOT_MS);
  const first = Math.max(Math.floor(startMs / SLOT_MS), last - maxSlots + 1);
  for (let slot = last; slot >= first; slot--) {
    for (const env of envs) {
      const r = requestAt(ctx, env, slot);
      if (r && r.startMs >= startMs && r.startMs < endMs) out.push(r);
    }
  }
  out.sort((a, b) => b.startMs - a.startMs);
  return { requests: out, sampledFromMs: Math.max(startMs, first * SLOT_MS) };
}

const parseTrace = (traceId: string): { env: DemoEnvKey; slot: number } | null => {
  if (!/^[0-9a-f]{32}$/.test(traceId)) return null;
  const env = DEMO_ENV_IDS[parseInt(traceId[23], 16)];
  return env ? { env, slot: parseInt(traceId.slice(24), 16) } : null;
};

const isBrowser = (r: Req) => r.route.browser;

function servicesOf(r: Req): string[] {
  const set = new Set<string>([r.route.service]);
  if (r.crosses) r.route.hops.forEach((h) => set.add(h.service));
  return [...(isBrowser(r) ? [DEMO_FRONTEND] : []), ...DEMO_BACKENDS.filter((s) => set.has(s))];
}

function summary(r: Req): TraceSummary {
  const browser = isBrowser(r);
  const n = Math.floor(hash01(r.slot, 13, r.envIndex) * 1000);
  const trace = traceOf(r);
  const end = Math.max(...trace.spans.map((s) => s.startMs + s.durationMs));
  return {
    traceId: r.traceId,
    rootService: browser ? DEMO_FRONTEND : r.route.service,
    rootOperation: r.route.op,
    startMs: r.startMs,
    durationMs: Math.round((end - r.startMs) * 100) / 100,
    rootDurationMs: trace.spans[0].durationMs,
    spanCount: trace.spans.length,
    errorCount: trace.spans.filter((s) => s.status === "error").length,
    services: servicesOf(r),
    userId: browser || r.route.charges ? r.customer.id : "",
    sessionId: browser ? `sess-${hex(r.slot * 3 + r.envIndex, 10, 53)}` : "",
    country: browser ? r.customer.country : "",
    region: "",
    city: browser ? r.customer.city : "",
    client: browser ? DEMO_CLIENTS[n % DEMO_CLIENTS.length] : NO_CLIENT,
    frontendVersion: browser ? r.version : "",
    backendBuilds: servicesOf(r).filter((s) => s !== DEMO_FRONTEND).map((service) => ({ service, version: r.version })),
    source: "",
    operatorData: false,
  };
}

/** A request's spans: (the browser's,) the root, its queries, the charge, and — when it crosses — the queue hop into worker and inventory. */
function traceOf(r: Req): Trace {
  const spans: Span[] = [];
  const env = ENV_NAME[r.env];
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
      resource: { "service.name": service, "service.version": r.version, "deployment.environment.name": env },
      events: [],
      source: "",
      operatorData: false,
      ...extra,
    };
    spans.push(s);
    return s;
  };
  const id = (n: number) => hex((r.slot * 3 + r.envIndex) * 64 + n, 16, 91);
  const rpc = !r.route.op.startsWith("GET ") && r.route.op.includes("/");
  const errorStatus = r.failed ? { status: "error" as const, statusMessage: DEMO_INCIDENT.error } : {};
  const browserSpan = isBrowser(r)
    ? span(id(60), "", r.route.op.startsWith("GET ") ? r.route.op.replace("{slug}", "linen-tote-bag") : `HTTP POST ${r.route.op}`, DEMO_FRONTEND, -4, r.durationMs + 8, {
        kind: "client",
        attributes: { "http.request.method": r.route.op.startsWith("GET ") ? "GET" : "POST" },
        ...errorStatus,
      })
    : null;
  const root = span(id(0), browserSpan?.spanId ?? "", r.route.op, r.route.service, 0, r.durationMs, {
    kind: "server",
    attributes: rpc ? { "rpc.system": "connect_rpc", "rpc.method": r.route.op.split("/")[1] } : { "http.route": r.route.op.slice(4) },
    ...errorStatus,
  });
  const table = r.route.service === "inventory" ? ["SELECT stock", "UPDATE stock"] : r.route.op.includes("products") ? ["SELECT products", "SELECT stock"] : ["SELECT customers", "INSERT orders", "SELECT orders"];
  // The budget left for queries: the total, minus the charge's spans and the crossing's.
  const fixed = 1 + (browserSpan ? 1 : 0) + (r.route.charges ? (r.failed || r.retried ? 3 : 2) : 0) + (r.crosses ? 4 : 0);
  const queries = Math.max(1, Math.min(r.crosses ? 3 : r.spanCount - fixed, 6));
  let at = 0.6;
  const queryShare = r.route.charges ? 0.25 : 0.7;
  for (let q = 0; q < queries; q++) {
    const d = (Math.min(r.durationMs, r.route.base * 3) * queryShare) / queries;
    span(id(1 + q), root.spanId, table[q % table.length], r.route.service, at, d * 0.85, { kind: "client", attributes: { "db.system.name": "postgresql" } });
    at += d;
  }
  if (r.route.charges) {
    const chargeMs = r.failed ? DEMO_INCIDENT.deadlineMs - at : r.retried ? DEMO_INCIDENT.retryMs + 110 : r.durationMs * 0.5;
    const charge = span(id(10), root.spanId, "payments.Charge", r.route.service, at, chargeMs, {
      kind: "client",
      attributes: { "peer.service": DEMO_DEPENDENCIES.payments, "server.address": DEMO_PAYMENTS_HOST },
      ...errorStatus,
      events: r.failed || r.retried ? [{ timeMs: r.startMs + at + DEMO_INCIDENT.retryMs, name: "retry", attributes: { attempt: "2" } }] : [],
    });
    const post = (n: number, start: number, d: number, timedOut: boolean, last: boolean) =>
      span(id(n), charge.spanId, `POST https://${DEMO_PAYMENTS_HOST}/v1/charges`, r.route.service, start, d, {
        kind: "client",
        attributes: { "http.request.method": "POST", "server.address": DEMO_PAYMENTS_HOST, "http.response.status_code": timedOut ? "" : "200" },
        // A timed-out attempt the retry recovered is not an error of the request's: only a failed request's spans are.
        ...(timedOut && r.failed ? { status: "error" as const, statusMessage: last ? "context deadline exceeded" : `timed out after ${DEMO_INCIDENT.retryMs / 1000}s` } : {}),
        ...(timedOut && !r.failed ? { events: [{ timeMs: r.startMs + start + d, name: "timeout", attributes: { "error.type": "timeout" } }] } : {}),
      });
    if (r.failed || r.retried) {
      post(11, at + 0.4, DEMO_INCIDENT.retryMs, true, false);
      post(12, at + DEMO_INCIDENT.retryMs + 1, r.failed ? chargeMs - DEMO_INCIDENT.retryMs - 2 : 105, r.failed, true);
    } else {
      post(11, at + 0.4, chargeMs - 1, false, true);
    }
    at += chargeMs;
  }
  if (r.crosses) {
    const pub = span(id(20), root.spanId, "nats publish orders.created", r.route.service, Math.min(at, r.durationMs - 1), 0.6, { kind: "producer" });
    const consume = span(id(21), pub.spanId, "orders.created process", "worker", r.durationMs + 1, 28, { kind: "consumer" });
    const call = span(id(22), consume.spanId, "inventory.v1.InventoryService/Reserve", "worker", r.durationMs + 2, 24, { kind: "client", attributes: { "rpc.system": "connect_rpc" } });
    const server = span(id(23), call.spanId, "inventory.v1.InventoryService/Reserve", "inventory", r.durationMs + 2.5, 22, { kind: "server", attributes: { "rpc.system": "connect_rpc", "rpc.method": "Reserve" } });
    // The rest of the span budget: a statement per reserved line item.
    const lines = Math.max(1, r.spanCount - spans.length);
    for (let k = 0; k < lines; k++) {
      span(id(30 + k), server.spanId, "UPDATE stock", "inventory", r.durationMs + 3 + (k * 20) / lines, 20 / lines - 0.1, {
        kind: "client",
        attributes: { "db.system.name": "postgresql", "db.query.text": "UPDATE stock SET reserved = reserved + $1 WHERE sku = $2 AND on_hand - reserved >= $1" },
      });
    }
  }
  return { traceId: r.traceId, spans, truncated: false };
}

/** The lines a request logged, oldest first. */
function logsOf(r: Req): LogRecord[] {
  const out: LogRecord[] = [];
  const product = DEMO_PRODUCTS[r.slot % DEMO_PRODUCTS.length];
  const line = (n: number, at: number, severity: Severity, service: string, body: string) =>
    out.push({
      id: `busy:${r.envIndex}:${r.slot}:${n}`,
      timeMs: r.startMs + at,
      severity,
      severityText: severity.toUpperCase(),
      service,
      body,
      traceId: r.traceId,
      spanId: hex((r.slot * 3 + r.envIndex) * 64, 16, 91),
      attributes: { procedure: r.route.op.startsWith("GET ") ? r.route.op : `/${r.route.op}`, "http.status_code": r.failed ? "504" : "200" },
      resource: { "service.name": service, "k8s.pod.name": `${service}-7d9f8c-${["q2m4z", "k8x1p"][r.slot % 2]}`, "deployment.environment.name": ENV_NAME[r.env] },
      source: "",
      operatorData: false,
    });
  const service = r.route.service;
  const label = r.route.op.startsWith("GET ") ? r.route.op : r.route.op.split("/").pop()!;
  line(0, 0.2, "info", service, `${label} started`);
  if (!r.failed && !r.retried && r.durationMs > r.route.base * 4) line(1, r.durationMs * 0.5, "warn", service, `slow query (${Math.round(r.durationMs * 0.6)}ms): SELECT … FROM ${service === "inventory" ? "stock" : "orders"} WHERE customer_id = $1`);
  if (r.failed || r.retried) line(2, DEMO_INCIDENT.retryMs, "warn", service, DEMO_INCIDENT.retryWarning);
  if (r.failed) {
    line(3, r.durationMs - 1, "error", service, DEMO_INCIDENT.error);
  } else {
    if (r.retried) line(3, r.durationMs * 0.9, "info", service, "payments: charge succeeded on attempt 2");
    if (hash01(r.slot, 14, r.envIndex) < 0.15) line(4, 1, "debug", service, `cache hit for customer ${r.customer.id}`);
    const done: Record<string, string> = {
      CreateOrder: `order ${r.order} created for ${r.customer.id}`,
      GetOrder: `order ${r.order} read`,
      ListOrders: `listed ${10 + (r.slot % 15)} orders for ${r.customer.id}`,
      CancelOrder: `order ${r.order} cancelled`,
      Checkout: `checkout completed: order ${r.order}`,
      GetStock: `stock read for ${product.sku}`,
      Reserve: `reserved ${1 + (r.slot % 3)} items of ${product.sku}`,
      "fulfilment.tick": `fulfilment tick: ${r.slot % 7} orders handed to the carrier`,
    };
    line(5, r.durationMs * 0.95, "info", service, done[label] ?? `rendered ${product.name}`);
  }
  if (r.crosses) {
    line(6, r.durationMs + 20, "info", "worker", `order event ${r.order} processed`);
    line(7, r.durationMs + 24, "info", "inventory", `reserved ${1 + (r.slot % 3)} items for order ${r.order}`);
  }
  return out;
}

// ── The toured trace ─────────────────────────────────────────────────────────

/**
 * The trace the tour opens: prod's newest failing CreateOrder (TOURED_AGO_MS ago),
 * span for span what journeys/forge.tsx points at — the api's method span
 * (a2), a SELECT (b1), the charge (b3) and its two timed-out attempts, the
 * order event (b4) the worker consumed (d1), its Connect call (d3) into
 * inventory's server span (e1), method span (e2) and UPDATE (e3).
 */
function touredTrace(ctx: Ctx): Trace {
  const t0 = ctx.touredAtMs;
  const deadline = DEMO_INCIDENT.deadlineMs;
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
    resource: { "service.name": service, "service.version": DEMO_INCIDENT.release, "deployment.environment.name": "prod" },
    events: [],
    source: "",
    operatorData: false,
    ...extra,
  });
  const failed = { status: "error" as const, statusMessage: DEMO_INCIDENT.error };
  return {
    traceId: DEMO_TOURED_TRACE.traceId,
    truncated: false,
    spans: [
      span("a1", "", "orders.v1.OrdersService/CreateOrder", "api", 0, deadline + 12.4, { kind: "server", attributes: { "rpc.system": "connect_rpc", "rpc.method": "CreateOrder", "http.status_code": "504" }, ...failed }),
      span("a2", "a1", "orders.CreateOrder", "api", 1.0, deadline + 10.6, { kind: "internal", attributes: { "forge.method.id": "orders.v1.OrdersService/CreateOrder" } }),
      span("b1", "a2", "SELECT customers", "api", 3.1, 4.6, { attributes: { "db.system.name": "postgresql", "db.operation.name": "SELECT" } }),
      span("b2", "a2", "INSERT orders", "api", 8.2, 6.9, { attributes: { "db.system.name": "postgresql", "db.operation.name": "INSERT" } }),
      span("b3", "a2", "payments.Charge", "api", 16.0, deadline - 16.5, {
        attributes: { "peer.service": "payments", "server.address": DEMO_PAYMENTS_HOST },
        ...failed,
        events: [{ timeMs: t0 + 16 + DEMO_INCIDENT.retryMs, name: "retry", attributes: { attempt: "2" } }],
      }),
      span("c1", "b3", `POST https://${DEMO_PAYMENTS_HOST}/v1/charges`, "api", 16.4, DEMO_INCIDENT.retryMs, { attributes: { "http.request.method": "POST" }, status: "error", statusMessage: `timed out after ${DEMO_INCIDENT.retryMs / 1000}s` }),
      span("c2", "b3", `POST https://${DEMO_PAYMENTS_HOST}/v1/charges`, "api", 17.5 + DEMO_INCIDENT.retryMs, deadline - DEMO_INCIDENT.retryMs - 19, { attributes: { "http.request.method": "POST" }, status: "error", statusMessage: "context deadline exceeded" }),
      span("b4", "a2", "nats publish orders.created", "api", deadline + 2, 0.8, { kind: "producer", attributes: { "messaging.system": "nats" } }),
      span("d1", "b4", "orders.created process", "worker", deadline + 4.3, 28.7, { kind: "consumer", attributes: { "messaging.system": "nats" } }),
      span("d3", "d1", "inventory.v1.InventoryService/Reserve", "worker", deadline + 6.0, 19.6, { kind: "client", attributes: { "rpc.system": "connect_rpc", "rpc.service": "inventory.v1.InventoryService", "rpc.method": "Reserve" } }),
      span("e1", "d3", "inventory.v1.InventoryService/Reserve", "inventory", deadline + 6.8, 18.2, { kind: "server", attributes: { "rpc.system": "connect_rpc", "rpc.method": "Reserve", "http.status_code": "200" } }),
      span("e2", "e1", "inventory.Reserve", "inventory", deadline + 7.0, 17.7, { kind: "internal", attributes: { "forge.method.id": "inventory.v1.InventoryService/Reserve" } }),
      span("e3", "e2", "UPDATE stock", "inventory", deadline + 7.5, 16.7, {
        attributes: { "db.system.name": "postgresql", "db.operation.name": "UPDATE", "db.query.text": "UPDATE stock SET reserved = reserved + $1 WHERE sku = $2 AND on_hand - reserved >= $1" },
      }),
    ],
  };
}

function touredSummary(ctx: Ctx): TraceSummary {
  const trace = touredTrace(ctx);
  const end = Math.max(...trace.spans.map((s) => s.startMs + s.durationMs));
  const customer = DEMO_CUSTOMERS[0];
  return {
    traceId: trace.traceId,
    rootService: "api",
    rootOperation: "orders.v1.OrdersService/CreateOrder",
    startMs: ctx.touredAtMs,
    durationMs: Math.round((end - ctx.touredAtMs) * 100) / 100,
    rootDurationMs: trace.spans[0].durationMs,
    spanCount: trace.spans.length,
    errorCount: trace.spans.filter((s) => s.status === "error").length,
    services: ["api", "worker", "inventory"],
    userId: customer.id,
    sessionId: "",
    country: "",
    region: "",
    city: "",
    client: NO_CLIENT,
    frontendVersion: "",
    backendBuilds: ["api", "worker", "inventory"].map((service) => ({ service, version: DEMO_INCIDENT.release })),
    source: "",
    operatorData: false,
  };
}

/** What the toured request logged: five lines from three services. */
function touredLogs(ctx: Ctx): LogRecord[] {
  const t0 = ctx.touredAtMs;
  const d = DEMO_INCIDENT.deadlineMs;
  const lines: [string, number, Severity, string, string][] = [
    ["a1", 0.5, "info", "api", "CreateOrder started"],
    ["c1", 16.4 + DEMO_INCIDENT.retryMs, "warn", "api", DEMO_INCIDENT.retryWarning],
    ["c2", d - 1, "error", "api", DEMO_INCIDENT.error],
    ["d1", d + 31, "info", "worker", "order event ord_7q2k9f processed"],
    ["e2", d + 24.5, "info", "inventory", "reserved 2 items for order ord_7q2k9f"],
  ];
  return lines.map(([spanId, at, severity, service, body]) => ({
    id: `toured:${spanId}`,
    timeMs: t0 + at,
    severity,
    severityText: severity.toUpperCase(),
    service,
    body,
    traceId: DEMO_TOURED_TRACE.traceId,
    spanId,
    attributes: { "rpc.method": "CreateOrder", procedure: "/orders.v1.OrdersService/CreateOrder", "http.status_code": severity === "error" ? "504" : "200" },
    resource: { "service.name": service, "k8s.pod.name": `${service}-7d9f8c-q2m4z`, "deployment.environment.name": "prod" },
    source: "",
    operatorData: false,
  }));
}

// ── Profiles ─────────────────────────────────────────────────────────────────

type Stacks = [string[], number][];

/** Per service, per profile type: the frames it spends its time / memory / goroutines in. */
const PROFILES: Record<string, Record<string, Stacks>> = {
  api: {
    cpu: [
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "orders.(*Service).CreateOrder", "encoding/json.Marshal"], 38],
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "orders.(*Service).ListOrders", "pgx.(*Conn).Query"], 91],
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "orders.(*Service).GetOrder", "pgx.(*Conn).QueryRow"], 44],
      [["main", "net/http.(*conn).serve", "payments.(*Client).Charge", "net/http.(*Transport).roundTrip", "crypto/tls.(*Conn).Read"], 23],
      [["main", "net/http.(*conn).serve", "web.(*Products).Render", "html/template.(*Template).Execute"], 31],
      [["main", "runtime.gcBgMarkWorker"], 27],
    ],
    alloc_space: [
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "orders.(*Service).ListOrders", "pgx.(*Rows).Scan"], 412 << 20],
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "protojson.Marshal"], 268 << 20],
      [["main", "net/http.(*conn).serve", "web.(*Products).Render", "bytes.(*Buffer).grow"], 181 << 20],
      [["main", "net/http.(*conn).serve", "payments.(*Client).Charge", "encoding/json.Unmarshal"], 36 << 20],
    ],
    alloc_objects: [
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "orders.(*Service).ListOrders", "pgx.(*Rows).Scan"], 2_140_000],
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "protojson.Marshal"], 1_380_000],
      [["main", "net/http.(*conn).serve", "web.(*Products).Render", "html/template.(*state).walk"], 960_000],
    ],
    inuse_space: [
      [["main", "orders.NewService", "lru.New[...]"], 48 << 20],
      [["main", "pgxpool.NewWithConfig", "pgx.connect"], 22 << 20],
      [["main", "net/http.(*conn).serve", "bufio.NewReaderSize"], 9 << 20],
      [["main", "web.LoadTemplates", "html/template.(*Template).Parse"], 6 << 20],
    ],
    inuse_objects: [
      [["main", "orders.NewService", "lru.New[...]"], 182_000],
      [["main", "pgxpool.NewWithConfig", "pgx.connect"], 41_000],
      [["main", "net/http.(*conn).serve", "bufio.NewReaderSize"], 12_000],
    ],
    goroutine: [
      [["main", "net/http.(*conn).serve", "net/http.(*conn).readRequest"], 64],
      [["main", "net/http.(*conn).serve", "payments.(*Client).Charge", "net/http.(*persistConn).roundTrip"], 22],
      [["main", "pgxpool.(*Pool).backgroundHealthCheck"], 1],
      [["main", "nats.(*Conn).readLoop"], 2],
    ],
  },
  worker: {
    cpu: [
      [["main", "nats.(*Subscription).processMsg", "fulfilment.(*Worker).Process", "inventoryv1connect.(*inventoryServiceClient).Reserve"], 58],
      [["main", "nats.(*Subscription).processMsg", "fulfilment.(*Worker).Process", "encoding/json.Unmarshal"], 29],
      [["main", "lifecyclekit.Every", "fulfilment.(*Worker).Tick", "pgx.(*Conn).Query"], 21],
      [["main", "runtime.gcBgMarkWorker"], 9],
    ],
    alloc_space: [
      [["main", "nats.(*Subscription).processMsg", "fulfilment.(*Worker).Process", "encoding/json.Unmarshal"], 96 << 20],
      [["main", "lifecyclekit.Every", "fulfilment.(*Worker).Tick", "pgx.(*Rows).Scan"], 54 << 20],
      [["main", "nats.(*Conn).readLoop", "nats.(*msgParser).parse"], 31 << 20],
    ],
    alloc_objects: [
      [["main", "nats.(*Subscription).processMsg", "fulfilment.(*Worker).Process", "encoding/json.Unmarshal"], 610_000],
      [["main", "lifecyclekit.Every", "fulfilment.(*Worker).Tick", "pgx.(*Rows).Scan"], 280_000],
    ],
    inuse_space: [
      [["main", "nats.Connect", "nats.(*Conn).setup"], 8 << 20],
      [["main", "fulfilment.New", "make([]fulfilment.job)"], 3 << 20],
    ],
    inuse_objects: [
      [["main", "nats.Connect", "nats.(*Conn).setup"], 9_400],
      [["main", "fulfilment.New", "make([]fulfilment.job)"], 2_100],
    ],
    goroutine: [
      [["main", "fulfilment.(*Worker).Run", "fulfilment.(*Worker).Process"], 8],
      [["main", "nats.(*Conn).readLoop"], 1],
      [["main", "lifecyclekit.Every"], 1],
    ],
  },
  inventory: {
    cpu: [
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "inventory.(*Service).GetStock", "pgx.(*Conn).QueryRow"], 63],
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "inventory.(*Service).Reserve", "pgx.(*Tx).Exec"], 47],
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "proto.Marshal"], 12],
      [["main", "runtime.gcBgMarkWorker"], 8],
    ],
    alloc_space: [
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "inventory.(*Service).GetStock", "pgx.(*Rows).Scan"], 122 << 20],
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "proto.Marshal"], 64 << 20],
    ],
    alloc_objects: [
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "inventory.(*Service).GetStock", "pgx.(*Rows).Scan"], 720_000],
      [["main", "net/http.(*conn).serve", "connect.(*Handler).ServeHTTP", "proto.Marshal"], 330_000],
    ],
    inuse_space: [
      [["main", "inventory.NewService", "sync.Map.Store"], 14 << 20],
      [["main", "pgxpool.NewWithConfig", "pgx.connect"], 11 << 20],
    ],
    inuse_objects: [
      [["main", "inventory.NewService", "sync.Map.Store"], 52_000],
      [["main", "pgxpool.NewWithConfig", "pgx.connect"], 20_000],
    ],
    goroutine: [
      [["main", "net/http.(*conn).serve", "net/http.(*conn).readRequest"], 18],
      [["main", "pgxpool.(*Pool).backgroundHealthCheck"], 1],
    ],
  },
};

export const DEMO_PROFILE_TYPES = ["cpu", "alloc_space", "alloc_objects", "inuse_space", "inuse_objects", "goroutine"] as const;

const unitOf = (type: string) => (type === "cpu" ? "nanoseconds" : type.endsWith("_space") ? "bytes" : "none");

function profileOf(service: string, type: string): Flamegraph {
  const byType = PROFILES[service] ?? PROFILES.api;
  const kind = byType[type] ? type : "cpu";
  const stacks = byType[kind];
  const names = [...new Set(stacks.flatMap(([frames]) => frames))];
  const samples = stacks.map(([frames]) => frames.map((f) => names.indexOf(f)));
  // CPU weights are milliseconds of samples over the window, in nanoseconds.
  const weights = stacks.map(([, w]) => (kind === "cpu" ? w * 1e7 : w));
  const total = weights.reduce((a, b) => a + b, 0);
  const unit = unitOf(kind);
  return {
    speedscopeJson: JSON.stringify({
      $schema: "https://www.speedscope.app/file-format-schema.json",
      shared: { frames: names.map((name) => ({ name })) },
      profiles: [{ type: "sampled", name: `${service} ${kind}`, unit, startValue: 0, endValue: total, samples, weights }],
      name: `${service} ${kind}`,
      exporter: "forge demo",
    }),
    totalValue: total,
    unit,
    stackCount: stacks.length,
    availableProfileTypes: [...DEMO_PROFILE_TYPES],
    truncated: false,
  };
}

// ── Reads ────────────────────────────────────────────────────────────────────

/** What each service sends and declares (Service signals): every backend sends all three and declares them. */
export const DEMO_SIGNALS: ServiceSignalsResult = {
  truncated: false,
  services: DEMO_BACKENDS.map((service) => ({ service, emitting: ["logs", "profiles", "traces"], declared: ["logs", "profiles", "traces"], declarationKnown: true })),
};

const offsetOf = (cursor: string | undefined) => (cursor && /^busy:\d+$/.test(cursor) ? Number(cursor.slice(5)) : 0);

const envsOf = (envIds: readonly string[] | undefined): DemoEnvKey[] =>
  envIds?.length ? DEMO_ENV_IDS.filter((e) => envIds.includes(e)) : [...DEMO_ENV_IDS];

/** A group-by key's value on one trace. */
function groupValue(t: TraceSummary & { env: string }, key: string): string {
  switch (key.replace(/^(resource|span)\.attr\./, "")) {
    case "deployment.environment.name":
      return t.env;
    case "service.name":
      return t.rootService;
    case "service.version":
      return t.backendBuilds[0]?.version ?? t.frontendVersion;
    case "rpc.method":
      return t.rootOperation.split("/").pop() ?? "";
    case "status":
      return t.errorCount ? "error" : "ok";
    default:
      return "";
  }
}

function redOf(list: TraceSummary[], seconds: number): TraceRed {
  const durations = list.map((t) => t.durationMs).sort((a, b) => a - b);
  const at = (p: number) => durations[Math.min(durations.length - 1, Math.floor(p * durations.length))] ?? 0;
  const errors = list.filter((t) => t.errorCount > 0).length;
  return {
    traceCount: list.length,
    errorTraceCount: errors,
    ratePerSecond: seconds > 0 ? list.length / seconds : 0,
    errorRate: list.length ? errors / list.length : 0,
    p50Ms: at(0.5),
    p95Ms: at(0.95),
    p99Ms: at(0.99),
  };
}

/** Prod's failed incident requests in [startMs, endMs), newest first (the toured one included): the incident issue's events. */
export function failedIncidentRequests(now: number, startMs: number, endMs: number, limit: number) {
  const ctx = ctxOf(now);
  const { requests } = requestsIn(ctx, startMs, endMs, ["env-prod"], 2_000);
  const out = requests
    .filter((r) => r.failed)
    .map((r) => ({ traceId: r.traceId, startMs: r.startMs, chargeSpanId: hex((r.slot * 3 + r.envIndex) * 64 + 10, 16, 91), method: r.route.op.split("/").pop() ?? "", sessionId: "", userId: r.customer.id }));
  if (ctx.touredAtMs >= startMs && ctx.touredAtMs < endMs) {
    out.push({ traceId: DEMO_TOURED_TRACE.traceId, startMs: ctx.touredAtMs, chargeSpanId: "b3", method: "CreateOrder", sessionId: "", userId: DEMO_CUSTOMERS[0].id });
  }
  return out.sort((a, b) => b.startMs - a.startMs).slice(0, limit);
}

type TelemetryApi = Pick<ObservabilityApi, "listTraces" | "getTrace" | "searchLogs" | "getServiceOverview" | "getFlamegraph" | "getServiceSignals">;

/** shop's traces, logs, service overview, profiles and signals, as of `now`. */
export function createBusyApp(now: number = demoNow()): TelemetryApi {
  const ctx = ctxOf(now);
  const touredInScope = (envIds: readonly string[], startMs: number, endMs: number) =>
    envsOf(envIds).includes("env-prod") && ctx.touredAtMs >= startMs && ctx.touredAtMs < endMs;
  return {
    listTraces: async (req) => {
      const envs = envsOf(req.envIds);
      const { requests, sampledFromMs } = requestsIn(ctx, req.window.startMs, req.window.endMs, envs);
      let all: (TraceSummary & { env: string })[] = requests.map((r) => ({ ...summary(r), env: ENV_NAME[r.env] }));
      if (touredInScope(req.envIds, req.window.startMs, req.window.endMs)) all.push({ ...touredSummary(ctx), env: "prod" });
      all.sort((a, b) => b.startMs - a.startMs);
      if (req.rootSpanName) all = all.filter((t) => t.rootOperation.toLowerCase().includes(req.rootSpanName.toLowerCase()));
      if (req.services?.length) all = all.filter((t) => t.services.some((s) => req.services.includes(s)));
      if (req.errorsOnly) all = all.filter((t) => t.errorCount > 0);
      if (req.minDurationMs) all = all.filter((t) => t.durationMs >= req.minDurationMs);
      if (req.userId) all = all.filter((t) => t.userId === req.userId);
      if (req.sessionId) all = all.filter((t) => t.sessionId === req.sessionId);
      if (req.browser) all = all.filter((t) => t.client.browserName === req.browser);
      if (req.os) all = all.filter((t) => t.client.osName === req.os);
      if (req.frontendVersion) all = all.filter((t) => t.frontendVersion === req.frontendVersion);
      if (req.backendVersion) all = all.filter((t) => t.backendBuilds.some((b) => b.version === req.backendVersion));
      const seconds = (req.window.endMs - req.window.startMs) / 1000;
      const red = redOf(all, seconds);
      // A window longer than the slots built: the counts are scaled to the whole window (latency is the sample's).
      if (sampledFromMs > req.window.startMs) {
        const whole = demoStats({ startMs: req.window.startMs, endMs: req.window.endMs, envIds: envs, now }).requests;
        const sampled = demoStats({ startMs: sampledFromMs, endMs: req.window.endMs, envIds: envs, now }).requests;
        const scale = sampled > 0 ? whole / sampled : 1;
        red.traceCount = Math.round(red.traceCount * scale);
        red.errorTraceCount = Math.round(red.errorTraceCount * scale);
        red.ratePerSecond = red.traceCount / seconds;
      }
      const groupBy = req.groupBy ?? [];
      const groups = new Map<string, { group: Record<string, string>; list: TraceSummary[] }>();
      if (groupBy.length) {
        for (const t of all) {
          const group = Object.fromEntries(groupBy.map((k) => [k, groupValue(t, k)]));
          const key = JSON.stringify(group);
          if (!groups.has(key)) groups.set(key, { group, list: [] });
          groups.get(key)!.list.push(t);
        }
      }
      const from = offsetOf(req.cursor);
      const limit = req.limit > 0 ? req.limit : 100;
      const page = all.slice(from, from + limit).map(({ env: _env, ...t }) => t);
      return {
        traces: page,
        nextCursor: from + limit < all.length ? `busy:${from + limit}` : "",
        red,
        redGroups: [...groups.values()].map((g) => ({ group: g.group, red: redOf(g.list, seconds) })),
      };
    },
    getTrace: async (req) => {
      if (req.traceId === DEMO_TOURED_TRACE.traceId) return touredTrace(ctx);
      const at = parseTrace(req.traceId);
      const r = at ? requestAt(ctx, at.env, at.slot) : null;
      return r && r.traceId === req.traceId ? traceOf(r) : { traceId: req.traceId, spans: [], truncated: false };
    },
    searchLogs: async (req) => {
      let records: LogRecord[];
      if (req.traceId === DEMO_TOURED_TRACE.traceId) {
        records = touredLogs(ctx);
      } else if (req.traceId) {
        const at = parseTrace(req.traceId);
        const r = at ? requestAt(ctx, at.env, at.slot) : null;
        records = r && r.traceId === req.traceId ? logsOf(r) : [];
      } else {
        const envs = envsOf(req.envIds);
        records = requestsIn(ctx, req.window.startMs, req.window.endMs, envs).requests.flatMap(logsOf);
        if (touredInScope(req.envIds, req.window.startMs, req.window.endMs)) records.push(...touredLogs(ctx));
      }
      records = records.filter((r) => !!req.traceId || (r.timeMs >= req.window.startMs && r.timeMs < req.window.endMs)).sort((a, b) => b.timeMs - a.timeMs);
      if (req.spanId) records = records.filter((r) => r.spanId === req.spanId);
      if (req.services?.length) records = records.filter((r) => req.services.includes(r.service));
      if (req.severities?.length) records = records.filter((r) => req.severities.includes(r.severity));
      if (req.query) records = records.filter((r) => r.body.toLowerCase().includes(req.query.toLowerCase()));
      const from = offsetOf(req.cursor);
      const limit = req.limit > 0 ? req.limit : 200;
      return { records: records.slice(from, from + limit), nextCursor: from + limit < records.length ? `busy:${from + limit}` : "" };
    },
    getServiceOverview: async (req) => {
      const groupBy = req.groupBy ?? [];
      const byEnv = groupBy.length === 1 && groupBy[0].replace(/^resource\.attr\./, "") === "deployment.environment.name";
      const names = DEMO_BACKENDS.filter((s) => !req.services?.length || req.services.includes(s));
      const steps = 30;
      const stepMs = (req.window.endMs - req.window.startMs) / steps;
      const cells: { name: string; envIds: DemoEnvKey[]; group: Record<string, string> }[] = byEnv
        ? names.flatMap((name) => envsOf(req.envIds).map((env) => ({ name, envIds: [env], group: { [groupBy[0]]: ENV_NAME[env] } })))
        : names.map((name) => ({ name, envIds: envsOf(req.envIds), group: {} }));
      const services: ServiceHealth[] = cells.map(({ name, envIds, group }) => {
        const q = { startMs: req.window.startMs, endMs: req.window.endMs, envIds, service: name, now };
        const whole = demoStats(q);
        const trend = demoTrend(q, steps);
        const timestamps = trend.map((b) => Math.round(b.startMs / 1000));
        const requestTrend = trend.map((b) => b.stats.requests);
        const errorTrend = trend.map((b) => b.stats.errors);
        // The headline is the sparkline's sum, so the two can never disagree.
        const requestCount = requestTrend.reduce((a, b) => a + b, 0);
        const errorCount = errorTrend.reduce((a, b) => a + b, 0);
        return {
          name,
          group,
          requestCount,
          requestRate: requestCount / ((req.window.endMs - req.window.startMs) / 1000),
          errorCount,
          errorRate: requestCount ? errorCount / requestCount : 0,
          p50Ms: whole.p50Ms,
          p95Ms: whole.p95Ms,
          p99Ms: whole.p99Ms,
          errorLogCount: errorCount,
          timestamps,
          requestTrend,
          errorTrend,
          rateSeries: requestTrend.map((n) => n / (stepMs / 1000)),
          errorRateSeries: trend.map((b) => (b.stats.requests ? b.stats.errors / b.stats.requests : null)),
          source: "",
          operatorData: false,
        };
      });
      return { services, trendStepSeconds: Math.round(stepMs / 1000) };
    },
    getFlamegraph: async (req) => profileOf(req.traceId ? "api" : req.service || "api", req.profileType || "cpu"),
    getServiceSignals: async () => DEMO_SIGNALS,
  };
}

/** Test seams. */
export const busyAppInternals = {
  ctxOf,
  requestAt,
  requestsIn: (now: number, startMs: number, endMs: number, envs?: readonly DemoEnvKey[]) => requestsIn(ctxOf(now), startMs, endMs, envs).requests,
  traceOf,
  logsOf,
  summary,
  touredTrace: (now: number) => touredTrace(ctxOf(now)),
};
