// Copyright (c) 2026 Reliant Labs
import { describe, expect, it } from "vitest";
import { fixtureGraph, graphEdge, graphNode, GRAPH_RPC } from "../../__fixtures__/fixtureGraph";
import { callTotals, edgeLabel, graphModel, MAX_GRAPH_NODES, MAX_GRAPH_EDGES, methodTotals } from "../graphModel";
import { histogramQuantile } from "../graphStats";

describe("bounded graph model", () => {
  it("deduplicates without adding aggregate counters and excludes conflicts", () => {
    const g = fixtureGraph(); const first = g.edges[0];
    g.edges.push({ ...first });
    expect(graphModel(g).edges.find((e) => e.id === first.id)?.count).toBe(100n);
    g.edges.push({ ...first, count: 101n });
    const result = graphModel(g);
    expect(result.conflicts).toBe(1);
    expect(result.edges.some((e) => e.id === first.id)).toBe(false);
  });
  it("filters full case-sensitive RPCs, not basename collisions", () => {
    const g = fixtureGraph(); g.edges.push(graphEdge("other", "api", "orders", { rpcId: "other.v1.OrdersService/GetOrder" }));
    const result = graphModel(g, { rpcId: GRAPH_RPC });
    expect(result.edges.every((e) => e.rpcId === GRAPH_RPC)).toBe(true);
    expect(graphModel(g, { rpcId: "GetOrder" }).edges).toHaveLength(0);
  });
  it("separates declared and async semantics, preserves exact large counters", () => {
    const g = fixtureGraph();
    expect(graphModel(g).edges.some((e) => e.evidence === "declared")).toBe(false);
    expect(graphModel(g, { declared: true }).edges.some((e) => e.evidence === "declared")).toBe(true);
    const async = g.edges.find((e) => e.kind === "async-send")!;
    expect(edgeLabel({ ...async, count: 9007199254740993n })).toContain("9007199254740993 publish");
    expect(edgeLabel(g.edges.find((e) => e.evidence === "declared")!)).not.toContain("0");
  });
  it("excludes missing and cross-environment endpoints instead of inventing links", () => {
    const g = fixtureGraph(); g.nodes.push(graphNode("other-env", "process", "env-other"));
    g.edges.push(graphEdge("cross", "api", "other-env"), graphEdge("missing", "api", "missing"));
    const result = graphModel(g);
    expect(result.dangling).toBe(2);
    expect(result.edges.some((e) => e.id === "cross" || e.id === "missing")).toBe(false);
  });
  it("bounds deterministic layout including cycles and disconnected nodes", () => {
    const g = fixtureGraph();
    g.nodes = Array.from({ length: 400 }, (_, i) => graphNode(`n${i.toString().padStart(3, "0")}`));
    g.edges = Array.from({ length: 400 }, (_, i) => graphEdge(`e${i}`, g.nodes[i].id, g.nodes[(i + 1) % 400].id));
    const a = graphModel(g); const b = graphModel({ ...g, nodes: [...g.nodes].reverse(), edges: [...g.edges].reverse() });
    expect(a.nodes.length).toBeLessThanOrEqual(MAX_GRAPH_NODES);
    expect(a.edges.length).toBeLessThanOrEqual(MAX_GRAPH_EDGES);
    expect(a.omittedNodes).toBeGreaterThan(0);
    expect(a.omittedEdges).toBeGreaterThan(0);
    expect(a.nodes).toEqual(b.nodes);
    expect(a.nodes.every((n) => Number.isFinite(n.position.x) && Number.isFinite(n.position.y))).toBe(true);
  });
  it("faults filter does not silently label unknown status as fault or success", () => {
    const g = fixtureGraph(); g.edges = [graphEdge("unknown", "api", "orders", { faults: 0n, unknownStatus: 100n })];
    expect(graphModel(g, { errorsOnly: true }).edges).toHaveLength(0);
    expect(graphModel(g).edges[0].unknownStatus).toBe(100n);
  });
  it("hides unresolved callers by default, still counting their requests", () => {
    const g = fixtureGraph();
    const hidden = graphModel(g);
    expect(hidden.nodes.some((n) => n.id === "unknown")).toBe(false);
    expect(hidden.hiddenUnresolved).toBe(1);
    // orders: 100 from api plus 3 from the unresolved caller.
    expect(hidden.stats.get("orders")?.requests).toBe(103n);
    const shown = graphModel(g, { showUnresolved: true });
    expect(shown.nodes.some((n) => n.id === "unknown")).toBe(true);
    expect(shown.hiddenUnresolved).toBe(0);
  });
  it("folds every edge between two nodes into one link with summed traffic", () => {
    const g = fixtureGraph();
    g.edges.push(graphEdge("api-orders-2", "api", "orders", { rpcId: "shop.v1.OrdersService/ListOrders", count: 50n, faults: 0n }));
    const link = graphModel(g).links.find((l) => l.source === "api" && l.target === "orders")!;
    expect(link.edges).toHaveLength(2);
    expect(link.count).toBe(150n);
    expect(link.faults).toBe(3n);
    expect(link.duration?.count).toBe(200n);
  });
  it("counts a link from one viewpoint: a client attempt and its server request are one call", () => {
    const g = fixtureGraph();
    g.edges.push(graphEdge("api-orders-client", "api", "orders", { unit: "client_attempt", viewpoint: "client", count: 104n, faults: 5n }));
    const link = graphModel(g).links.find((l) => l.source === "api" && l.target === "orders")!;
    expect(link.edges).toHaveLength(2);
    expect(link).toMatchObject({ count: 100n, faults: 3n, unit: "server_request", viewpoint: "server" });
    expect(link.duration?.count).toBe(100n);
  });
  it("chooses the viewpoint per method: a method only its caller saw still counts", () => {
    const g = fixtureGraph();
    // GetOrder: server 100 (fixture) and client 104 for the same calls; ListOrders: client view only.
    g.edges.push(graphEdge("api-orders-client", "api", "orders", { unit: "client_attempt", viewpoint: "client", count: 104n, faults: 5n }));
    g.edges.push(graphEdge("api-orders-list", "api", "orders", { rpcId: "shop.v1.OrdersService/ListOrders", unit: "client_attempt", viewpoint: "client", count: 20n, faults: 4n }));
    // A publish counts messages, not requests: never part of this sum.
    g.edges.push(graphEdge("api-orders-publish", "api", "orders", { kind: "async-send", rpcId: "", unit: "publish", viewpoint: "producer", count: 9n, faults: 0n }));
    const link = graphModel(g).links.find((l) => l.source === "api" && l.target === "orders")!;
    expect(link).toMatchObject({ count: 120n, faults: 7n, unit: "server_request", viewpoint: "server" });
    expect(link.duration?.count).toBe(200n);
  });
  it("lists each method once and counts a node's calls from one viewpoint per method", () => {
    const g = fixtureGraph();
    g.edges.push(graphEdge("api-orders-client", "api", "orders", { unit: "client_attempt", viewpoint: "client", count: 104n, faults: 5n }));
    g.edges.push(graphEdge("api-orders-list", "api", "orders", { rpcId: "shop.v1.OrdersService/ListOrders", unit: "client_attempt", viewpoint: "client", count: 20n, faults: 4n }));
    expect(methodTotals(g.edges.filter((e) => e.source === "api" && e.target === "orders")).map(({ rpcId, count, faults }) => ({ rpcId, count, faults })))
      .toEqual([{ rpcId: GRAPH_RPC, count: 100n, faults: 3n }, { rpcId: "shop.v1.OrdersService/ListOrders", count: 20n, faults: 4n }]);
    expect(callTotals(g.edges, "api").get("orders")).toBe(120n);
  });
  it("never sums different units between the same endpoints into one count", () => {
    const g = fixtureGraph();
    g.edges.push(graphEdge("orders-queue-delivery", "orders", "queue", { kind: "async-consume", unit: "delivery", viewpoint: "consumer", rpcId: "", count: 40n }));
    const link = graphModel(g).links.find((l) => l.source === "orders" && l.target === "queue")!;
    expect(link).toMatchObject({ count: 40n, unit: "delivery", viewpoint: "consumer" });
    // Only the caller's view exists: it stands in, still unsummed with nothing.
    const clientOnly = fixtureGraph();
    clientOnly.edges = [graphEdge("c", "api", "orders", { unit: "client_attempt", viewpoint: "client", count: 7n })];
    expect(graphModel(clientOnly).links[0]).toMatchObject({ count: 7n, unit: "client_attempt", viewpoint: "client" });
  });
  it("keeps a service whose only matching traffic is with a hidden unresolved caller", () => {
    const g = fixtureGraph();
    const rpc = "shop.v1.OrdersService/Refund";
    g.edges.push(graphEdge("unknown-refund", "unknown", "orders", { rpcId: rpc, count: 2n, faults: 2n }));
    const result = graphModel(g, { rpcId: rpc });
    expect(result.nodes.map((n) => n.id)).toEqual(["orders"]);
    expect(result.links).toHaveLength(0);
    expect(result.hiddenUnresolved).toBe(1);
    expect(result.stats.get("orders")?.requests).toBe(2n);
    expect(graphModel(g, { rpcId: rpc, showUnresolved: true }).nodes.map((n) => n.id).sort()).toEqual(["orders", "unknown"]);
  });
  it("lays callers left of callees and datastores in the last column", () => {
    const x = new Map(graphModel(fixtureGraph()).nodes.map((n) => [n.id, n.position.x]));
    expect(x.get("browser")!).toBeLessThan(x.get("api")!);
    expect(x.get("api")!).toBeLessThan(x.get("orders")!);
    expect(x.get("orders")!).toBeLessThan(x.get("postgres")!);
    expect(Math.max(...x.values())).toBe(x.get("postgres"));
  });
  it("interpolates percentiles inside histogram buckets", () => {
    const h = { version: 1, boundsSeconds: [0.1, 1], buckets: [0n, 10n, 0n], count: 10n, sumSeconds: 5 };
    expect(histogramQuantile(h, 0.5)).toBeCloseTo(550);
    expect(histogramQuantile({ ...h, buckets: [0n, 0n, 10n] }, 0.95)).toBe(1000);
    expect(histogramQuantile({ ...h, buckets: [0n, 10n] }, 0.5)).toBeUndefined();
  });
});
