// Copyright (c) 2026 Reliant Labs

/**
 * The Observability Overview's rules (U6c → U6e), pure so they are tested
 * alone (__tests__/obsOverviewModel.test.ts): which endpoints are the
 * slowest, and how the service map is shrunk into a thumbnail.
 */

import type { RPCCatalog, ServiceGraph } from "@/services/controlPlane/observability/graphTypes";

import { graphModel, isUnresolved } from "../Graph/graphModel";
import { histogramQuantile, shortService } from "../Graph/graphStats";

export interface SlowEndpoint {
  id: string;
  service: string;
  method: string;
  p95Ms: number;
  calls: number;
  /** Server faults / calls; null with no calls. */
  errorRate: number | null;
}

/**
 * The methods the catalog observed, slowest first by p95 (each from its
 * server-viewpoint duration histogram). A method with no usable histogram has
 * no p95 to rank by and is left out, never shown as 0 ms. `minCalls` keeps a
 * method called twice from topping the list with one slow call.
 */
export function slowestEndpoints(catalog: RPCCatalog | undefined, limit = 5, minCalls = 10): SlowEndpoint[] {
  if (!catalog) return [];
  return catalog.methods
    .filter((m) => m.evidence === "observed" && Number(m.observedCount) >= minCalls)
    .flatMap((m) => {
      const p95 = histogramQuantile(m.duration, 0.95);
      if (p95 === undefined) return [];
      const calls = Number(m.observedCount);
      return [{ id: m.id, service: m.service, method: m.method, p95Ms: p95, calls, errorRate: calls ? Number(m.faults) / calls : null }];
    })
    .sort((a, b) => b.p95Ms - a.p95Ms || a.id.localeCompare(b.id))
    .slice(0, limit);
}

export interface ThumbNode {
  id: string;
  label: string;
  kind: string;
  x: number;
  y: number;
}
export interface ThumbLink {
  id: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  /** A link whose faults are 1% of its calls or more. */
  failing: boolean;
}

export const THUMB_W = 320;
export const THUMB_H = 160;
const PAD = 18;

/**
 * The service map at thumbnail size: the full graph's own layered layout
 * (graphModel → layoutGraph), scaled to fit THUMB_W × THUMB_H, unresolved
 * peers left out. The shape matches the full graph's, so the thumbnail is a
 * preview of what the link opens, not a different drawing.
 */
export function graphThumbnail(graph: ServiceGraph | undefined): { nodes: ThumbNode[]; links: ThumbLink[] } {
  if (!graph) return { nodes: [], links: [] };
  const model = graphModel(graph);
  const positioned = model.nodes.filter((n) => !isUnresolved(n));
  if (positioned.length === 0) return { nodes: [], links: [] };
  const xs = positioned.map((n) => n.position.x);
  const ys = positioned.map((n) => n.position.y);
  const [minX, maxX, minY, maxY] = [Math.min(...xs), Math.max(...xs), Math.min(...ys), Math.max(...ys)];
  const sx = maxX > minX ? (THUMB_W - 2 * PAD) / (maxX - minX) : 0;
  const sy = maxY > minY ? (THUMB_H - 2 * PAD) / (maxY - minY) : 0;
  const scale = Math.min(sx || Infinity, sy || Infinity);
  const k = Number.isFinite(scale) ? scale : 1;
  // Centre the drawing in the frame on each axis.
  const offX = (THUMB_W - (maxX - minX) * k) / 2;
  const offY = (THUMB_H - (maxY - minY) * k) / 2;
  const at = new Map(positioned.map((n) => [n.id, { x: offX + (n.position.x - minX) * k, y: offY + (n.position.y - minY) * k }]));
  const nodes = positioned.map((n) => ({ id: n.id, label: shortService(n.label), kind: n.kind, ...at.get(n.id)! }));
  const links = model.links.flatMap((l) => {
    const a = at.get(l.source);
    const b = at.get(l.target);
    if (!a || !b) return [];
    return [{ id: l.id, x1: a.x, y1: a.y, x2: b.x, y2: b.y, failing: l.count > 0n && Number(l.faults) / Number(l.count) >= 0.01 }];
  });
  return { nodes, links };
}
