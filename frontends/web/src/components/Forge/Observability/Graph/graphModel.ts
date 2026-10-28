// Copyright (c) 2026 Reliant Labs
import type { GraphEdge, GraphNode, ServiceGraph } from "@/services/controlPlane/observability/graphTypes";

export const MAX_GRAPH_NODES = 80;
export const MAX_GRAPH_EDGES = 160;
export interface GraphFilters { rpcId?: string; errorsOnly?: boolean; declared?: boolean; internal?: boolean }
export interface PositionedNode extends GraphNode { position: { x: number; y: number } }

/** Exact duplicate rows collapse, contradictory IDs are excluded, never summed.
 * The server owns aggregation; UI dedup must not double counts on replay. */
function canonical<T extends { id: string }>(rows: readonly T[]): { rows: T[]; conflicts: number } {
  const byId = new Map<string, T>();
  const payloads = new Map<string, string>();
  const conflicts = new Set<string>();
  for (const row of rows) {
    if (!row.id) continue;
    const payload = JSON.stringify(row, (_, value) => typeof value === "bigint" ? value.toString() : value);
    if (payloads.has(row.id) && payloads.get(row.id) !== payload) conflicts.add(row.id);
    else { payloads.set(row.id, payload); byId.set(row.id, row); }
  }
  return { rows: [...byId.values()].filter((r) => !conflicts.has(r.id)).sort((a, b) => a.id.localeCompare(b.id)), conflicts: conflicts.size };
}

/** Bounded deterministic breadth-first layout, including cycles/disconnected nodes.
 * No recursive traversal or iterative force simulation. O(V + E) after sorting. */
export function layoutGraph(nodes: GraphNode[], edges: GraphEdge[]): PositionedNode[] {
  const outgoing = new Map<string, string[]>();
  const indegree = new Map(nodes.map((n) => [n.id, 0]));
  for (const edge of edges) {
    if (!indegree.has(edge.source) || !indegree.has(edge.target)) continue;
    outgoing.set(edge.source, [...(outgoing.get(edge.source) ?? []), edge.target]);
    indegree.set(edge.target, (indegree.get(edge.target) ?? 0) + 1);
  }
  const depth = new Map<string, number>();
  const visit = (root: string) => {
    const queue = [root];
    depth.set(root, 0);
    for (let i = 0; i < queue.length; i++) {
      const id = queue[i];
      for (const next of outgoing.get(id) ?? []) {
        if (depth.has(next)) continue;
        depth.set(next, Math.min((depth.get(id) ?? 0) + 1, 5));
        queue.push(next);
      }
    }
  };
  for (const node of nodes) if (indegree.get(node.id) === 0 && !depth.has(node.id)) visit(node.id);
  for (const node of nodes) if (!depth.has(node.id)) visit(node.id);
  const lanes = new Map<number, number>();
  return nodes.map((node) => {
    const col = depth.get(node.id) ?? 0;
    const row = lanes.get(col) ?? 0;
    lanes.set(col, row + 1);
    return { ...node, position: { x: col * 260, y: row * 110 } };
  });
}

export function graphModel(graph: ServiceGraph, filters: GraphFilters = {}) {
  // Input already bounded by the API. Defend rendering independently.
  const inputLimit = 2000;
  const ns = canonical(graph.nodes.slice(0, inputLimit));
  const es = canonical(graph.edges.slice(0, inputLimit));
  const known = new Map(ns.rows.map((n) => [n.id, n]));
  let dangling = 0;
  const eligible = es.rows.filter((e) => {
    if (!known.has(e.source) || !known.has(e.target)) { dangling++; return false; }
    if (e.envId !== known.get(e.source)?.envId || e.envId !== known.get(e.target)?.envId) { dangling++; return false; }
    return (!filters.rpcId || e.rpcId === filters.rpcId) &&
      (!filters.errorsOnly || e.faults > 0n) &&
      (filters.declared || e.evidence !== "declared") &&
      (filters.internal || e.kind !== "internal");
  });
  const edges = eligible.slice(0, MAX_GRAPH_EDGES);
  const incident = new Set(edges.flatMap((e) => [e.source, e.target]));
  const filtered = !!filters.rpcId || !!filters.errorsOnly;
  const candidates = ns.rows.filter((n) => (filters.internal || incident.has(n.id) || (n.kind !== "rpc" && n.kind !== "method")) && (!filtered || incident.has(n.id)));
  // Prioritize incident nodes over isolated inventory entries.
  candidates.sort((a, b) => Number(incident.has(b.id)) - Number(incident.has(a.id)) || a.id.localeCompare(b.id));
  const nodes = candidates.slice(0, MAX_GRAPH_NODES);
  const visible = new Set(nodes.map((n) => n.id));
  const visibleEdges = edges.filter((e) => visible.has(e.source) && visible.has(e.target));
  return {
    nodes: layoutGraph(nodes, visibleEdges), edges: visibleEdges,
    conflicts: ns.conflicts + es.conflicts, dangling,
    omittedNodes: candidates.length - nodes.length,
    omittedEdges: eligible.length - visibleEdges.length,
    inputTruncated: graph.nodes.length > inputLimit || graph.edges.length > inputLimit,
  };
}

export function edgeLabel(edge: GraphEdge): string {
  if (edge.evidence === "declared") return "Declared · not observed";
  if (edge.evidence === "unknown") return "Unknown evidence";
  return `${edge.count.toString()} ${edge.unit || "observations"} · ${edge.viewpoint || "unknown viewpoint"}`;
}
