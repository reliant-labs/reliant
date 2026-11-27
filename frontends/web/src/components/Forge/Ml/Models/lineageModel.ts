// Copyright (c) 2026 Reliant Labs

/**
 * The lineage graph's layout, as a pure function of GetMlLineage's nodes and
 * edges: left to right by the contract's columns (inputs, phases,
 * checkpoints, versions, releases, services). A column whose nodes feed
 * each other (pretrain → sft → rl) spreads into sub-columns in that order,
 * so every edge still runs left to right. Rows are placed by the barycentre
 * of each node's neighbours (two sweeps), then pushed apart, so a chain
 * reads on one line and crossings stay few.
 */

import { MlLineageEdgeKind, MlSubjectKind, type MlLineageEdge, type MlLineageNode, type MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

export const COLUMN_LABELS = ["Inputs", "Phases", "Checkpoints", "Versions", "Releases", "Services"] as const;

export const NODE_W = 138;
export const NODE_H = 46;
export const GAP_X = 20;
export const ROW_H = 58;
export const HEADER_H = 28;

export interface PlacedNode {
  node: MlLineageNode;
  id: string;
  x: number;
  y: number;
  column: number;
}

export interface PlacedEdge {
  edge: MlLineageEdge;
  key: string;
  /** SVG path from the source's right side to the target's left side. */
  path: string;
  mid: { x: number; y: number };
}

export interface LineageLayout {
  nodes: PlacedNode[];
  edges: PlacedEdge[];
  headers: { label: string; x: number; width: number }[];
  width: number;
  height: number;
}

/** Each node's depth inside its column (longest chain of same-column edges into it). */
function subDepths(nodes: readonly MlLineageNode[], edges: readonly MlLineageEdge[]): Map<string, number> {
  const col = new Map(nodes.map((n) => [n.subject!.id, n.column]));
  const depth = new Map(nodes.map((n) => [n.subject!.id, 0]));
  // Relax |nodes| times: enough for any DAG, and terminates on a cycle.
  for (let i = 0; i < nodes.length; i++) {
    let changed = false;
    for (const e of edges) {
      if (!col.has(e.from) || col.get(e.from) !== col.get(e.to)) continue;
      const d = depth.get(e.from)! + 1;
      if (d > depth.get(e.to)! && d < nodes.length) {
        depth.set(e.to, d);
        changed = true;
      }
    }
    if (!changed) break;
  }
  return depth;
}

export function layoutLineage(nodes: readonly MlLineageNode[], edges: readonly MlLineageEdge[]): LineageLayout {
  const present = nodes.filter((n) => n.subject?.id);
  const ids = new Set(present.map((n) => n.subject!.id));
  const live = edges.filter((e) => ids.has(e.from) && ids.has(e.to));
  const depth = subDepths(present, live);

  // Lanes: (column, depth) pairs that hold a node, left to right.
  const laneKey = (n: MlLineageNode) => `${n.column}:${depth.get(n.subject!.id)}`;
  const lanes = [...new Set(present.map(laneKey))].sort((a, b) => {
    const [ca, da] = a.split(":").map(Number);
    const [cb, db] = b.split(":").map(Number);
    return ca - cb || da - db;
  });
  const laneX = new Map(lanes.map((l, i) => [l, i * (NODE_W + GAP_X)]));
  const byLane = new Map<string, MlLineageNode[]>(lanes.map((l) => [l, present.filter((n) => laneKey(n) === l)]));

  const preds = new Map<string, string[]>();
  const succs = new Map<string, string[]>();
  for (const e of live) {
    preds.set(e.to, [...(preds.get(e.to) ?? []), e.from]);
    succs.set(e.from, [...(succs.get(e.from) ?? []), e.to]);
  }

  const y = new Map<string, number>();
  present.forEach((n, i) => y.set(n.subject!.id, i * ROW_H));
  const place = (lane: string, by: Map<string, string[]>) => {
    const items = byLane.get(lane)!.map((n, i) => {
      const ns = (by.get(n.subject!.id) ?? []).filter((id) => y.has(id));
      const bary = ns.length ? ns.reduce((s, id) => s + y.get(id)!, 0) / ns.length : (y.get(n.subject!.id) ?? i * ROW_H);
      return { id: n.subject!.id, bary, i };
    });
    items.sort((a, b) => a.bary - b.bary || a.i - b.i);
    let last = -Infinity;
    for (const it of items) {
      const at = Math.max(it.bary, last + ROW_H);
      y.set(it.id, at);
      last = at;
    }
  };
  // Initial order: each lane stacked; then forward by predecessors, back by successors, forward again.
  for (const lane of lanes) byLane.get(lane)!.forEach((n, i) => y.set(n.subject!.id, i * ROW_H));
  for (const lane of lanes.slice(1)) place(lane, preds);
  for (const lane of [...lanes].reverse().slice(1)) place(lane, succs);
  for (const lane of lanes.slice(1)) place(lane, preds);

  const minY = Math.min(...[...y.values()]);
  const placed: PlacedNode[] = present.map((n) => ({ node: n, id: n.subject!.id, column: n.column, x: laneX.get(laneKey(n))!, y: y.get(n.subject!.id)! - minY + HEADER_H }));
  const at = new Map(placed.map((p) => [p.id, p]));

  const placedEdges: PlacedEdge[] = live.map((e, i) => {
    const a = at.get(e.from)!;
    const b = at.get(e.to)!;
    const x1 = a.x + NODE_W;
    const y1 = a.y + NODE_H / 2;
    const x2 = b.x;
    const y2 = b.y + NODE_H / 2;
    const dx = Math.max(24, Math.abs(x2 - x1) / 2);
    return { edge: e, key: `${e.from}->${e.to}#${i}`, path: `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`, mid: { x: (x1 + x2) / 2, y: (y1 + y2) / 2 } };
  });

  const headers = COLUMN_LABELS.flatMap((label, c) => {
    const xs = lanes.filter((l) => Number(l.split(":")[0]) === c).map((l) => laneX.get(l)!);
    if (!xs.length) return [];
    const x = Math.min(...xs);
    return [{ label, x, width: Math.max(...xs) - x + NODE_W }];
  });

  return {
    nodes: placed,
    edges: placedEdges,
    headers,
    width: lanes.length ? lanes.length * (NODE_W + GAP_X) - GAP_X : 0,
    height: placed.length ? Math.max(...placed.map((p) => p.y)) + NODE_H : 0,
  };
}

/**
 * The nodes that stand for `subject` on its own graph: its node, or (an
 * attempt, a rank, an unpromoted checkpoint, a run) the phase or run nodes
 * that hold it.
 */
export function focusIds(subject: Pick<MlSubject, "kind" | "id">, nodes: readonly MlLineageNode[]): Set<string> {
  const ids = nodes.map((n) => n.subject?.id ?? "");
  if (ids.includes(subject.id)) return new Set([subject.id]);
  const parts = subject.id.split("/");
  if (subject.kind === MlSubjectKind.RUN) return new Set(ids.filter((id) => id.startsWith(`${subject.id}/`)));
  if (subject.kind === MlSubjectKind.MODEL) return new Set(ids.filter((id) => id.startsWith(`${subject.id}/`)));
  const phase = parts.slice(0, 2).join("/");
  if (ids.includes(phase)) return new Set([phase]);
  return new Set();
}

export const EDGE_LABELS: Record<number, string> = {
  [MlLineageEdgeKind.TRAINED_ON]: "trained on",
  [MlLineageEdgeKind.INITIALIZED_FROM]: "initialized from",
  [MlLineageEdgeKind.RESUMED_FROM]: "resumed from",
  [MlLineageEdgeKind.PRODUCED]: "produced",
  [MlLineageEdgeKind.EVALUATED]: "evaluated",
  [MlLineageEdgeKind.PROMOTED_TO]: "promoted to",
  [MlLineageEdgeKind.SHIPPED_IN]: "shipped in",
  [MlLineageEdgeKind.SERVES]: "serves",
  [MlLineageEdgeKind.DISTILLED_FROM]: "distilled from",
  [MlLineageEdgeKind.BUILT_FROM]: "built from",
};

/** An edge as a sentence: "trained on · 85% of the mix". */
export function edgeText(e: Pick<MlLineageEdge, "kind" | "label">): string {
  const kind = EDGE_LABELS[e.kind] ?? "linked";
  return e.label && e.label !== kind ? `${kind} · ${e.label}` : kind;
}

/** A node status as a tone. */
export const statusTone = (status: string) => (status === "failed" ? "danger" : status === "running" ? "info" : status === "serving" || status === "ok" ? "success" : "neutral");
