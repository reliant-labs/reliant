// Copyright (c) 2026 Reliant Labs
// Test/DEV preview only. Never imported by production query adapters.
import type { GraphEdge, GraphNode, GraphQuality, RPCCatalog, ServiceGraph } from "@/services/controlPlane/observability/graphTypes";
export const GRAPH_RPC = "shop.v1.OrdersService/GetOrder";
export const GRAPH_QUALITY: GraphQuality = { origin: "PRE_TAIL_RECEIVED", lossUnknown: true, samplingUnknown: true, truncated: false,
  provisional: true, duplicates: 2n, conflicts: 0n, unmatched: 3n, rejected: 0n, overflow: 0n,
  aggregatedThroughMs: Date.UTC(2026, 9, 27, 12), warnings: [], availableMinutes: 13, requestedMinutes: 15 };
/** 100 server durations: 60 ≤ 10ms, 30 ≤ 50ms, 9 ≤ 250ms, 1 ≤ 1s. */
export const GRAPH_HISTOGRAM = { version: 1, boundsSeconds: [0.01, 0.05, 0.25, 1], buckets: [60n, 30n, 9n, 1n, 0n], count: 100n, sumSeconds: 3.2 };
export const graphNode = (id: string, kind: GraphNode["kind"] = "process", envId = "env-prod"): GraphNode => ({
  id, kind, envId, label: id, parentId: "", rpcId: "", sourceUnavailableReason: "Exact source mapping unavailable" });
export const graphEdge = (id: string, source: string, target: string, patch: Partial<GraphEdge> = {}): GraphEdge => ({
  id, source, target, envId: "env-prod", kind: "sync", evidence: "observed", rpcId: GRAPH_RPC, unit: "server_request",
  viewpoint: "server", cohort: "env-agent", count: 100n, faults: 3n, userOutcomes: 1n, cancelled: 1n, unknownStatus: 2n,
  matched: 97n, unmatched: 3n, duration: GRAPH_HISTOGRAM, exemplars: [{ traceId: "a".repeat(32), spanId: "b".repeat(16), availabilityUnknown: true }],
  firstSeenMs: Date.UTC(2026, 9, 27, 11, 45), lastSeenMs: Date.UTC(2026, 9, 27, 12), ...patch });
export function fixtureGraph(empty = false, unknown = false): ServiceGraph {
  return { nodes: empty ? [] : [graphNode("browser", "frontend"), graphNode("api"), graphNode("orders"), graphNode("postgres", "database"), graphNode("payments", "external"), graphNode("queue", "broker"), graphNode("unknown", "unknown")],
    edges: empty ? [] : [graphEdge("browser-api", "browser", "api", { cohort: "browser" }), graphEdge("api-orders", "api", "orders"),
      graphEdge("orders-db", "orders", "postgres", { kind: "database", unit: "database_operation", viewpoint: "client", rpcId: "" }),
      graphEdge("declared", "orders", "payments", { kind: "declared", evidence: "declared", count: 0n, faults: 0n, matched: 0n, unmatched: 0n, rpcId: "", exemplars: [] }),
      graphEdge("async", "orders", "queue", { kind: "async-send", unit: "publish", viewpoint: "producer", rpcId: "" }),
      graphEdge("unmatched", "unknown", "orders", { kind: "sync", count: 3n, matched: 0n, unmatched: 3n })],
    quality: unknown ? null : { ...GRAPH_QUALITY, truncated: unknown },
    window: { startMs: Date.UTC(2026, 9, 27, 11, 45), endMs: Date.UTC(2026, 9, 27, 12) } };
}
const DECLARED = JSON.stringify({ schema: "forge.dev/service-catalog/v1", module: "example.com/shop",
  processes: [{ id: "orders", root: "Orders API", command: ["server", "api"], components: ["Orders"],
    serves: [{ service: "shop.v1.OrdersService", route_set: "UserRoutes", audience: "user", resolution: "declared_route_set", component: "Orders" }], clients: [], source: { path: "internal/app/roots.go", line: 4 } }],
  services: [{ name: "shop.v1.OrdersService", proto_file: "shop/v1/orders.proto", methods: [{ id: GRAPH_RPC, name: "GetOrder", input: "shop.v1.GetOrderRequest", output: "shop.v1.GetOrderResponse", client_streaming: false, server_streaming: false, idempotency: "NO_SIDE_EFFECTS", auth_required: true }],
    descriptor: { Schemas: { "shop.v1.GetOrderRequest": [{ name: "id", kind: "string", validate_options: "[(buf.validate.field).string.min_len = 1]" }], "shop.v1.GetOrderResponse": [{ name: "id", kind: "string" }] } } }],
  components: [{ id: "Orders", name: "Orders", kind: "handler", type: "*orders.Service", source: { path: "internal/handlers/orders/service.go", line: 20 }, methods: [] }], bindings: [], clients: [], diagnostics: [] });
export function fixtureCatalog(empty = false, unknown = false): RPCCatalog {
  return { methods: empty ? [] : [{ id: GRAPH_RPC, service: "shop.v1.OrdersService", method: "GetOrder", processIds: ["orders"], evidence: "observed", observedCount: 100n, faults: 3n, userOutcomes: 1n, duration: GRAPH_HISTOGRAM, inputType: "shop.v1.GetOrderRequest", outputType: "shop.v1.GetOrderResponse", streaming: "unary", sourceUnavailableReason: "No exact source mapping", idempotency: "NO_SIDE_EFFECTS", authRequired: true, servesProcessIds: ["orders"], canCallProcessIds: ["api"] }],
    quality: unknown ? null : GRAPH_QUALITY, declaredStatus: unknown ? "unavailable" : "available", unavailableReason: unknown ? "No verified hosted artifact catalog" : "",
    declaredCatalogs: unknown || empty ? [] : [{ envId: "env-prod", releaseId: "release-fixture", bundleDigest: "sha256:fixture", repository: "example.com/shop", commit: "a".repeat(40), json: DECLARED }] };
}
