// Copyright (c) 2026 Reliant Labs
import { describe, expect, it } from "vitest";
import { fixtureGraph, graphEdge, graphNode, GRAPH_RPC } from "../../__fixtures__/fixtureGraph";
import { edgeLabel, graphModel, MAX_GRAPH_NODES, MAX_GRAPH_EDGES } from "../graphModel";

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
});
