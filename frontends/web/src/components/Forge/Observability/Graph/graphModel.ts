// Copyright (c) 2026 Reliant Labs
import type { GraphEdge, GraphNode, ServiceGraph } from "@/services/controlPlane/observability/graphTypes";
import { sumHistograms, type Histogram } from "./graphStats";

export const MAX_GRAPH_NODES = 80;
export const MAX_GRAPH_EDGES = 500;
export interface GraphFilters { rpcId?: string; errorsOnly?: boolean; declared?: boolean; internal?: boolean; showUnresolved?: boolean }
export interface PositionedNode extends GraphNode { position: { x: number; y: number }; layer: number }

/** Traffic into a node, from its server viewpoint, and everything it sends. */
export interface NodeStats { requests: bigint; faults: bigint; duration?: Histogram; outbound: bigint }

/**
 * One drawn connection: every observed edge between the same two nodes
 * (one per RPC, cohort and viewpoint on the wire) folded into a single line,
 * so a pair of services is one stroke, not a bundle of overlapping labels.
 *
 * count, faults and duration come from ONE viewpoint and unit: the edges of
 * that pair's most authoritative viewpoint (see VIEWPOINT_RANK), summed across
 * RPCs and cohorts, which partition traffic. The wire contract forbids summing
 * viewpoints — a client attempt and the server request it became are the same
 * call — and different units count different things.
 */
export interface GraphLink {
  id: string;
  source: string;
  target: string;
  edges: GraphEdge[];
  count: bigint;
  faults: bigint;
  evidence: GraphEdge["evidence"];
  kind: GraphEdge["kind"];
  unit: string;
  viewpoint: string;
  duration?: Histogram;
}

// The server saw the call complete, so its counts, outcomes and durations are
// the authority; the caller's view stands in only where no server span exists.
const VIEWPOINT_RANK: Record<string, number> = { server: 0, consumer: 1, client: 2, producer: 3, internal: 4 };

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

/** The graph's synthetic endpoints for traffic whose other side was never seen. */
export function isUnresolved(node: Pick<GraphNode, "kind" | "label">): boolean {
  return node.kind === "unknown" || node.label === "unknown caller" || node.label === "unresolved peer";
}

const SINK_KINDS: ReadonlySet<GraphNode["kind"]> = new Set(["database", "external"]);
// Tie-break order inside a layer before crossing reduction: callers first, stores last.
const KIND_RANK: Partial<Record<GraphNode["kind"], number>> = { frontend: 0, api: 1, process: 2, service: 2, rpc: 3, method: 4, workflow: 5, broker: 5, database: 7, external: 8, unknown: 9 };
export const COLUMN_GAP = 300;
export const ROW_GAP = 92;

/**
 * Deterministic layered (Sugiyama-style) layout: frontends on the left,
 * callers before callees by longest path, datastores in the last column, then
 * a few barycenter sweeps to cut crossings. Iterative and O(sweeps·(V+E)) on
 * the bounded node set; cycles are broken at DFS back edges.
 */
export function layoutGraph(nodes: GraphNode[], links: Pick<GraphLink, "source" | "target">[]): PositionedNode[] {
  const sorted = [...nodes].sort((a, b) => a.id.localeCompare(b.id));
  const ids = new Set(sorted.map((n) => n.id));
  const kind = new Map(sorted.map((n) => [n.id, n.kind]));
  const out = new Map<string, string[]>(sorted.map((n) => [n.id, []]));
  const pairs = [...links].filter((l) => ids.has(l.source) && ids.has(l.target) && l.source !== l.target)
    .sort((a, b) => a.source.localeCompare(b.source) || a.target.localeCompare(b.target));
  for (const l of pairs) out.get(l.source)!.push(l.target);

  // Break cycles: iterative DFS, dropping edges into a node still on the stack.
  const state = new Map<string, 1 | 2>();
  const dag = new Map<string, string[]>(sorted.map((n) => [n.id, []]));
  const roots = [...sorted].sort((a, b) => (KIND_RANK[a.kind] ?? 6) - (KIND_RANK[b.kind] ?? 6) || a.id.localeCompare(b.id));
  for (const root of roots) {
    if (state.has(root.id)) continue;
    const stack: { id: string; i: number }[] = [{ id: root.id, i: 0 }];
    state.set(root.id, 1);
    while (stack.length) {
      const top = stack[stack.length - 1];
      const next = out.get(top.id)![top.i++];
      if (next === undefined) { state.set(top.id, 2); stack.pop(); continue; }
      if (state.get(next) === 1) continue; // back edge
      dag.get(top.id)!.push(next);
      if (!state.has(next)) { state.set(next, 1); stack.push({ id: next, i: 0 }); }
    }
  }

  // Longest-path layering over the DAG (Kahn order).
  const indegree = new Map(sorted.map((n) => [n.id, 0]));
  for (const targets of dag.values()) for (const t of targets) indegree.set(t, indegree.get(t)! + 1);
  const layer = new Map(sorted.map((n) => [n.id, 0]));
  const queue = sorted.filter((n) => indegree.get(n.id) === 0).map((n) => n.id);
  for (let i = 0; i < queue.length; i++) {
    const id = queue[i];
    for (const t of dag.get(id)!) {
      layer.set(t, Math.max(layer.get(t)!, layer.get(id)! + 1));
      indegree.set(t, indegree.get(t)! - 1);
      if (indegree.get(t) === 0) queue.push(t);
    }
  }
  // Datastores and external peers that only receive calls share the last column.
  const isSink = (id: string) => SINK_KINDS.has(kind.get(id)!) && out.get(id)!.length === 0;
  const connected = new Set(pairs.flatMap((l) => [l.source, l.target]));
  let last = 0;
  for (const n of sorted) if (!isSink(n.id) && connected.has(n.id)) last = Math.max(last, layer.get(n.id)!);
  for (const n of sorted) {
    if (isSink(n.id)) layer.set(n.id, last + 1);
    else if (!connected.has(n.id)) layer.set(n.id, n.kind === "frontend" ? 0 : Math.min(1, last));
  }

  // Order inside layers: kind/label first, then barycenter sweeps.
  const layers: string[][] = [];
  for (const n of [...sorted].sort((a, b) => (KIND_RANK[a.kind] ?? 6) - (KIND_RANK[b.kind] ?? 6) || a.label.localeCompare(b.label) || a.id.localeCompare(b.id))) {
    (layers[layer.get(n.id)!] ??= []).push(n.id);
  }
  const preds = new Map<string, string[]>(sorted.map((n) => [n.id, []]));
  for (const l of pairs) preds.get(l.target)!.push(l.source);
  const index = new Map<string, number>();
  const reindex = () => layers.forEach((row) => row?.forEach((id, i) => index.set(id, i)));
  reindex();
  for (let sweep = 0; sweep < 6; sweep++) {
    const down = sweep % 2 === 0;
    const order = down ? layers.keys() : [...layers.keys()].reverse();
    for (const l of order) {
      const row = layers[l];
      if (!row) continue;
      const bary = new Map(row.map((id) => {
        const near = (down ? preds.get(id)! : out.get(id)!).filter((o) => index.has(o) && layer.get(o) !== l);
        return [id, near.length ? near.reduce((s, o) => s + index.get(o)!, 0) / near.length : index.get(id)!];
      }));
      row.sort((a, b) => bary.get(a)! - bary.get(b)! || index.get(a)! - index.get(b)! || a.localeCompare(b));
      row.forEach((id, i) => index.set(id, i));
    }
  }

  const byId = new Map(sorted.map((n) => [n.id, n]));
  const positioned: PositionedNode[] = [];
  layers.forEach((row, l) => row?.forEach((id, i) => {
    positioned.push({ ...byId.get(id)!, layer: l, position: { x: l * COLUMN_GAP, y: (i - (row.length - 1) / 2) * ROW_GAP } });
  }));
  return positioned.sort((a, b) => a.id.localeCompare(b.id));
}

/**
 * The edges a statistic over `edges` may sum: the observed edges of their most
 * authoritative viewpoint and that edge's unit. `first` names the cohort (the
 * first edge in that order, observed or not, so a declared-only group still
 * has a kind and unit). Every place that sums traffic goes through here.
 */
export function authoritativeCohort(edges: GraphEdge[]): { first: GraphEdge; counted: GraphEdge[] } {
  const observed = edges.filter((e) => e.evidence === "observed");
  const rank = (e: GraphEdge) => VIEWPOINT_RANK[e.viewpoint] ?? Object.keys(VIEWPOINT_RANK).length;
  const first = [...(observed.length ? observed : edges)]
    .sort((a, b) => rank(a) - rank(b) || a.viewpoint.localeCompare(b.viewpoint) || a.unit.localeCompare(b.unit) || a.id.localeCompare(b.id))[0];
  return { first, counted: observed.filter((e) => e.viewpoint === first.viewpoint && e.unit === first.unit) };
}

function linksOf(edges: GraphEdge[]): GraphLink[] {
  const byPair = new Map<string, GraphEdge[]>();
  for (const e of edges) {
    const key = `${e.source}\u0000${e.target}`;
    byPair.set(key, [...(byPair.get(key) ?? []), e]);
  }
  return [...byPair.values()].map((group) => {
    const observed = group.filter((e) => e.evidence === "observed");
    const { first, counted } = authoritativeCohort(group);
    return {
      id: `${first.source}->${first.target}`, source: first.source, target: first.target, edges: group,
      count: counted.reduce((s, e) => s + e.count, 0n), faults: counted.reduce((s, e) => s + e.faults, 0n),
      evidence: observed.length ? "observed" : group.some((e) => e.evidence === "declared") ? "declared" : "unknown",
      kind: first.kind, unit: first.unit, viewpoint: first.viewpoint, duration: sumHistograms(counted.map((e) => e.duration)),
    } satisfies GraphLink;
  }).sort((a, b) => a.id.localeCompare(b.id));
}

function statsOf(edges: GraphEdge[]): Map<string, NodeStats> {
  const stats = new Map<string, NodeStats & { hs: GraphEdge["duration"][] }>();
  const get = (id: string) => {
    let s = stats.get(id);
    if (!s) { s = { requests: 0n, faults: 0n, outbound: 0n, hs: [] }; stats.set(id, s); }
    return s;
  };
  for (const e of edges) {
    if (e.evidence !== "observed") continue;
    get(e.source).outbound += e.count;
    if (e.unit === "server_request") {
      const t = get(e.target);
      t.requests += e.count;
      t.faults += e.faults;
      t.hs.push(e.duration);
    }
  }
  return new Map([...stats].map(([id, { hs, ...s }]) => [id, { ...s, duration: sumHistograms(hs) }]));
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
  // Node stats count every eligible request, including ones from an
  // unresolved caller that the default view does not draw.
  const stats = statsOf(eligible);
  const hidden = new Set(filters.showUnresolved ? [] : ns.rows.filter(isUnresolved).map((n) => n.id));
  const shown = eligible.filter((e) => !hidden.has(e.source) && !hidden.has(e.target));
  const edges = shown.slice(0, MAX_GRAPH_EDGES);
  const incident = new Set(edges.flatMap((e) => [e.source, e.target]));
  // A known node whose matching traffic is only with a hidden unresolved
  // node still matches the filter: it stays on the map, and the hidden
  // count beside it offers to show the other end.
  const matched = new Set(eligible.flatMap((e) => [e.source, e.target]));
  const filtered = !!filters.rpcId || !!filters.errorsOnly;
  // An unconnected service still belongs on the map; an unconnected
  // database/peer/queue/rpc is only an endpoint of an edge filtered away.
  const inventory = (n: GraphNode) => n.kind === "process" || n.kind === "frontend" || n.kind === "api" || n.kind === "service";
  const candidates = ns.rows.filter((n) => !hidden.has(n.id) && (incident.has(n.id) || matched.has(n.id) || (!filtered && inventory(n))));
  // Prioritize incident nodes over isolated inventory entries.
  candidates.sort((a, b) => Number(incident.has(b.id)) - Number(incident.has(a.id)) || a.id.localeCompare(b.id));
  const nodes = candidates.slice(0, MAX_GRAPH_NODES);
  const visible = new Set(nodes.map((n) => n.id));
  const visibleEdges = edges.filter((e) => visible.has(e.source) && visible.has(e.target));
  const links = linksOf(visibleEdges);
  return {
    nodes: layoutGraph(nodes, links), edges: visibleEdges, links, stats,
    hiddenUnresolved: ns.rows.filter((n) => hidden.has(n.id) && eligible.some((e) => e.source === n.id || e.target === n.id)).length,
    conflicts: ns.conflicts + es.conflicts, dangling,
    omittedNodes: candidates.length - nodes.length,
    omittedEdges: shown.length - visibleEdges.length,
    inputTruncated: graph.nodes.length > inputLimit || graph.edges.length > inputLimit,
  };
}

export function edgeLabel(edge: GraphEdge): string {
  if (edge.evidence === "declared") return "Declared · not observed";
  if (edge.evidence === "unknown") return "Unknown evidence";
  return `${edge.count.toString()} ${edge.unit || "observations"} · ${edge.viewpoint || "unknown viewpoint"}`;
}

/** What a unit counts, for a person: "server_request" → "requests". */
export function unitLabel(unit: string): string {
  switch (unit) {
    case "server_request": case "client_attempt": return "requests";
    case "database_operation": return "queries";
    case "publish": return "messages sent";
    case "delivery": return "messages handled";
    case "method_invocation": return "calls";
    default: return "observations";
  }
}
