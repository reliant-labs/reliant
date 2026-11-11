// Copyright (c) 2025 Reliant Labs

/**
 * shop's service graph and RPC catalog (the Service map and the RPC
 * explorer), from the story's traffic model: every edge's count, faults and
 * latency are demoStats for the window, so the map, the explorer, the
 * overview and the traces agree. Aggregated through the window's end: never
 * stale.
 *
 *   shop-web → api            the storefront's calls (every api request)
 *   api → postgres            the orders queries
 *   api → payments            the charge (an external provider): the incident
 *   api → nats → worker       the order event
 *   api → inventory           stock reads
 *   worker → inventory        Reserve, from the order event
 *   inventory → postgres      the stock queries
 */

import type { GraphEdge, GraphNode, GraphQuality, RPCCatalog, RPCMethod, ServiceGraph } from "@/services/controlPlane/observability/graphTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { TimeWindow } from "@/services/controlPlane/observability/types";

import {
  DEMO_DEPENDENCIES,
  DEMO_ENV_IDS,
  DEMO_FRONTEND,
  DEMO_REPO,
  DEMO_ROUTES,
  DEMO_RPC_METHODS,
  demoHistogram,
  demoLedger,
  demoStats,
  type DemoEnvKey,
  type DemoStatsQuery,
} from "../shopStory";
import type { GraphKeys } from "./shopApi";

const BOUNDS = [0.01, 0.05, 0.25, 1, 2.5];

const histogram = (q: DemoStatsQuery) => {
  const h = demoHistogram(q, BOUNDS);
  return { version: 1, boundsSeconds: BOUNDS, buckets: h.buckets.map((b) => BigInt(b)), count: BigInt(h.count), sumSeconds: h.sumSeconds };
};

function quality(window: TimeWindow): GraphQuality {
  const minutes = Math.round((window.endMs - window.startMs) / 60_000);
  return {
    origin: "PRE_TAIL_RECEIVED",
    lossUnknown: false,
    samplingUnknown: false,
    truncated: false,
    provisional: false,
    duplicates: 0n,
    conflicts: 0n,
    unmatched: 0n,
    rejected: 0n,
    overflow: 0n,
    aggregatedThroughMs: window.endMs,
    warnings: [],
    availableMinutes: minutes,
    requestedMinutes: minutes,
  };
}

const envOf = (envIds: readonly string[]): DemoEnvKey => (DEMO_ENV_IDS.find((e) => envIds.includes(e)) ?? "env-prod") as DemoEnvKey;

function graphOf(window: TimeWindow, env: DemoEnvKey, now: number): ServiceGraph {
  const q = (extra: Partial<DemoStatsQuery>): DemoStatsQuery => ({ startMs: window.startMs, endMs: Math.min(window.endMs, now), envIds: [env], now, ...extra });
  const node = (id: string, kind: GraphNode["kind"]): GraphNode => ({ id, kind, envId: env, label: id, parentId: "", rpcId: "", sourceUnavailableReason: "" });
  const base = { envId: env, userOutcomes: 0n, cancelled: 0n, unknownStatus: 0n, unmatched: 0n, exemplars: [], firstSeenMs: window.startMs, lastSeenMs: Math.min(window.endMs, now) };
  const edge = (id: string, source: string, target: string, kind: GraphEdge["kind"], stats: DemoStatsQuery[], extra: Partial<GraphEdge> = {}, scale = 1): GraphEdge => {
    const all = stats.map(demoStats);
    const count = Math.round(all.reduce((a, s) => a + s.requests, 0) * scale);
    const faults = Math.round(all.reduce((a, s) => a + s.errors, 0) * scale);
    const h = histogram(stats[0]);
    return { ...base, id, source, target, kind, evidence: "observed", rpcId: "", unit: "server_request", viewpoint: "server", cohort: "env-agent", count: BigInt(count), faults: BigInt(faults), matched: BigInt(count), duration: h, ...extra };
  };
  const route = (method: string) => DEMO_ROUTES.find((r) => r.op.endsWith(method))!.op;
  const { database, broker, payments } = DEMO_DEPENDENCIES;
  const charging = [q({ service: "api", op: route("/CreateOrder") }), q({ service: "api", op: route("/Checkout") })];
  // A query does not fail when the request it serves does: database edges carry no faults.
  const db = { unit: "database_operation", viewpoint: "client", faults: 0n };
  return {
    nodes: [node(DEMO_FRONTEND, "frontend"), node("api", "process"), node("worker", "process"), node("inventory", "process"), node(database, "database"), node(payments, "external"), node(broker, "broker")],
    edges: [
      edge(`${DEMO_FRONTEND}-api`, DEMO_FRONTEND, "api", "sync", [q({ service: "api" })], { cohort: "browser", rpcId: "" }),
      edge("api-postgres", "api", database, "database", [q({ service: "api" })], db, 2.2),
      edge("api-payments", "api", payments, "external", charging, { rpcId: "", viewpoint: "client", unit: "client_request" }),
      edge("api-nats", "api", broker, "async-send", charging, { unit: "publish", viewpoint: "producer", faults: 0n }, 0.6),
      edge("nats-worker", broker, "worker", "async-consume", [q({ service: "worker", op: "orders.created process" })], { unit: "process", viewpoint: "consumer" }),
      edge("api-inventory", "api", "inventory", "sync", [q({ service: "inventory", op: route("/GetStock") })], { rpcId: route("/GetStock") }),
      edge("worker-inventory", "worker", "inventory", "sync", [q({ service: "inventory", op: route("/Reserve") })], { rpcId: route("/Reserve") }),
      edge("inventory-postgres", "inventory", database, "database", [q({ service: "inventory" })], db, 1.4),
    ],
    quality: quality(window),
    window,
  };
}

/** The catalog forge declared for shop: its processes, services and every method's schemas. */
function declaredCatalog(): string {
  const services = new Map<string, typeof DEMO_RPC_METHODS>();
  for (const m of DEMO_RPC_METHODS) services.set(m.service, [...(services.get(m.service) ?? []), m]);
  const short = (svc: string) => svc.split(".").pop()!.replace(/Service$/, "");
  const fields: Record<string, [string, string][]> = {
    CreateOrder: [["customer_id", "string"], ["items", "message"], ["currency", "string"]],
    GetOrder: [["id", "string"]],
    ListOrders: [["customer_id", "string"], ["page_size", "int32"], ["page_token", "string"]],
    CancelOrder: [["id", "string"], ["reason", "string"]],
    Checkout: [["cart_id", "string"], ["payment_method", "string"]],
    GetStock: [["sku", "string"]],
    Reserve: [["order_id", "string"], ["items", "message"]],
  };
  return JSON.stringify({
    schema: "forge.dev/service-catalog/v1",
    module: "github.com/acme/shop",
    processes: [
      { id: "api", root: "API", command: ["server", "api"], components: ["Orders", "Checkout"], serves: ["orders.v1.OrdersService", "checkout.v1.CheckoutService"].map((service) => ({ service, route_set: "UserRoutes", audience: "user", resolution: "declared_route_set", component: short(service) })), clients: [], source: { path: "internal/app/roots.go", line: 4 } },
      { id: "inventory", root: "Inventory", command: ["server", "inventory"], components: ["Inventory"], serves: [{ service: "inventory.v1.InventoryService", route_set: "InternalRoutes", audience: "internal", resolution: "declared_route_set", component: "Inventory" }], clients: [], source: { path: "internal/app/roots.go", line: 11 } },
      { id: "worker", root: "Fulfilment", command: ["worker", "fulfilment"], components: [], serves: [], clients: [], source: { path: "internal/app/roots.go", line: 17 } },
    ],
    services: [...services.entries()].map(([service, methods]) => ({
      name: service,
      proto_file: `proto/services/${service.split(".")[0]}/v1/${service.split(".")[0]}.proto`,
      methods: methods.map((m) => ({ id: m.id, name: m.method, input: `${service.split(".").slice(0, 2).join(".")}.${m.method}Request`, output: `${service.split(".").slice(0, 2).join(".")}.${m.method}Response`, client_streaming: false, server_streaming: false, idempotency: /^(Get|List)/.test(m.method) ? "NO_SIDE_EFFECTS" : "" })),
      descriptor: {
        Schemas: Object.fromEntries(
          methods.flatMap((m) => {
            const pkg = service.split(".").slice(0, 2).join(".");
            return [
              [`${pkg}.${m.method}Request`, (fields[m.method] ?? []).map(([name, kind]) => ({ name, kind }))],
              [`${pkg}.${m.method}Response`, [{ name: m.method.startsWith("List") ? "orders" : "id", kind: m.method.startsWith("List") ? "message" : "string" }]],
            ];
          }),
        ),
      },
    })),
    components: ["Orders", "Checkout", "Inventory"].map((c) => ({ id: c, name: c, kind: "handler", type: `*${c.toLowerCase()}.Service`, source: { path: `internal/handlers/${c.toLowerCase()}/service.go`, line: 20 }, methods: [] })),
    bindings: [],
    clients: [],
    diagnostics: [],
  });
}

function catalogOf(window: TimeWindow, envIds: readonly string[], now: number, req: { rpcService?: string; rpcIds?: string[]; services?: string[] }): RPCCatalog {
  const env = envOf(envIds);
  const methods: RPCMethod[] = DEMO_RPC_METHODS.filter((m) => (!req.rpcService || m.service === req.rpcService) && (!req.rpcIds?.length || req.rpcIds.includes(m.id)) && (!req.services?.length || req.services.includes(m.process))).map((m) => {
    const q: DemoStatsQuery = { startMs: window.startMs, endMs: Math.min(window.endMs, now), envIds: [env], service: m.process, op: m.id, now };
    const s = demoStats(q);
    return {
      id: m.id,
      service: m.service,
      method: m.method,
      processIds: [m.process],
      evidence: "observed",
      observedCount: BigInt(s.requests),
      faults: BigInt(s.errors),
      userOutcomes: 0n,
      duration: histogram(q),
      inputType: `${m.service.split(".").slice(0, 2).join(".")}.${m.method}Request`,
      outputType: `${m.service.split(".").slice(0, 2).join(".")}.${m.method}Response`,
      streaming: "unary",
      sourceUnavailableReason: "",
      idempotency: /^(Get|List)/.test(m.method) ? "NO_SIDE_EFFECTS" : "",
      authRequired: m.process === "api",
      servesProcessIds: [m.process],
      canCallProcessIds: m.process === "inventory" ? ["api", "worker"] : [DEMO_FRONTEND],
    };
  });
  const running = demoLedger(now).filter((p) => p.envId === env && p.current)[0];
  return {
    methods,
    quality: quality(window),
    declaredStatus: "available",
    unavailableReason: "",
    declaredCatalogs: running ? [{ envId: env, releaseId: `rel-${running.version}`, bundleDigest: `sha256:${running.commit.padEnd(64, "0")}`, repository: DEMO_REPO, commit: running.commit.padEnd(40, "0"), json: declaredCatalog() }] : [],
  };
}

export function shopGraph(now: number): Pick<ObservabilityApi, GraphKeys> {
  return {
    getServiceGraph: async (req) => graphOf(req.window, envOf(req.envIds), now),
    getServiceGraphCatalog: async (req) => catalogOf(req.window, req.envIds, now, req),
  };
}
