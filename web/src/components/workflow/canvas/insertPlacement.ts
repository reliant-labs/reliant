// Copyright (c) 2025 Reliant Labs

/**
 * Where a new step goes on the canvas, and what it is wired to.
 *
 * A step used to land at the viewport centre with no edge, so every add took
 * a second, fiddly drag before the step did anything (WORKFLOW_EDITOR_UX_REVIEW
 * §1 issue 3). Here a step always lands connected:
 *
 *   - after a node's output   ("+" on the output, or "Add step" with a node
 *                               selected): a new edge from that output;
 *   - spliced into an edge    ("+" on the edge): A → B becomes A → new → B;
 *   - otherwise after the end of the main path, so the canvas never grows a
 *     stray node. Only a canvas with no nodes at all places one freely.
 *
 * Placement is deliberately simple: the next column to the right, below any
 * existing branch, nudged down past anything it would cover. Splicing makes
 * room by shifting what is downstream to the right. Nothing else moves —
 * a hand-arranged canvas stays the user's.
 *
 * Pure functions over React Flow nodes and edges, so the rules are unit
 * tested without a canvas.
 */

import type { Edge, Node, XYPosition } from "@xyflow/react";

import { uniqueNodeId } from "../../../lib/actionNodeArgs";
import { isEntryFlowNodeType } from "../../../lib/workflow-flow";
import { getDimensionsForFlowType, SWITCH_GEOMETRY } from "../../../lib/workflow-node-dimensions";

/** Where the next step is inserted. */
export type InsertAnchor =
  /** After a node's output; `sourceHandle` names a Switch case. */
  | { kind: "after"; nodeId: string; sourceHandle?: string | null }
  /** Into an edge, between its two ends. */
  | { kind: "splice"; edgeId: string };

/** Horizontal gap between a node and the step placed after it. */
export const INSERT_GAP_X = 80;
/** Vertical gap between stacked nodes, and the margin no two nodes may share. */
export const INSERT_GAP_Y = 40;

const SWITCH_NODE_TYPE = "switchNode";

interface Size {
  width: number;
  height: number;
}

interface SwitchCase {
  id: string;
  condition?: string;
  label?: string;
}

/** A node's size: measured once React Flow has rendered it, estimated before. */
export function nodeSize(node: Node): Size {
  const estimate = getDimensionsForFlowType(node.type ?? "");
  return {
    width: node.measured?.width ?? node.width ?? estimate.width,
    height: node.measured?.height ?? node.height ?? estimate.height,
  };
}

function switchCases(node: Node | undefined): SwitchCase[] {
  if (node?.type !== SWITCH_NODE_TYPE) return [];
  const cases = (node.data as { cases?: SwitchCase[] } | undefined)?.cases;
  return Array.isArray(cases) ? cases : [];
}

/**
 * The step-id base for a node type. The Agent step's YAML type is `workflow`,
 * which is also the start node's id, so it is named for what it is.
 */
const ID_BASES: Record<string, string> = { workflow: "agent" };

/**
 * A readable, unique, CEL-safe id for a new step: `call_llm`, then
 * `call_llm_2`. It becomes the path every expression uses
 * (`nodes.call_llm.content`), so it should read like the step, not like a
 * timestamp.
 */
export function readableStepId(stepType: string, taken: Iterable<string>): string {
  const raw = (ID_BASES[stepType] ?? stepType).replace(/[^a-zA-Z0-9]+/g, "_").replace(/^_+|_+$/g, "").toLowerCase();
  const base = raw === "" ? "step" : /^[a-z]/.test(raw) ? raw : `step_${raw}`;
  return uniqueNodeId(base, taken);
}

/** True when an edge leaves `nodeId` through `handle` (null: its only output). */
function leavesThrough(edge: Edge, nodeId: string, handle: string | null | undefined): boolean {
  return edge.source === nodeId && (edge.sourceHandle ?? null) === (handle ?? null);
}

/**
 * The output a new step attaches to when none was named. A Switch's first
 * case that leads nowhere yet, else its first case; any other node has one.
 */
export function defaultSourceHandle(node: Node, edges: readonly Edge[]): string | null {
  const cases = switchCases(node);
  if (cases.length === 0) return null;
  const free = cases.find((c) => !edges.some((e) => leavesThrough(e, node.id, c.id)));
  return (free ?? cases[0]!).id;
}

function anchorIsValid(anchor: InsertAnchor, nodes: readonly Node[], edges: readonly Edge[]): boolean {
  if (anchor.kind === "after") return nodes.some((n) => n.id === anchor.nodeId);
  const edge = edges.find((e) => e.id === anchor.edgeId);
  return !!edge && nodes.some((n) => n.id === edge.source) && nodes.some((n) => n.id === edge.target);
}

/**
 * The end of the main path: of the nodes reachable from the start, the one
 * with nothing after it that is furthest along (most edges from the start,
 * then furthest right). In a graph that only loops, the rightmost reachable
 * node. With no start node, the rightmost node.
 */
export function mainPathTail(nodes: readonly Node[], edges: readonly Edge[]): Node | undefined {
  if (nodes.length === 0) return undefined;
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const entry = nodes.find((n) => isEntryFlowNodeType(n.type));
  const rightmost = (candidates: Node[]) =>
    candidates.reduce((best, n) => (n.position.x > best.position.x ? n : best), candidates[0]!);
  if (!entry) return rightmost([...nodes]);

  const depth = new Map<string, number>([[entry.id, 0]]);
  const queue = [entry.id];
  while (queue.length > 0) {
    const id = queue.shift()!;
    for (const edge of edges) {
      if (edge.source !== id || depth.has(edge.target) || !byId.has(edge.target)) continue;
      depth.set(edge.target, depth.get(id)! + 1);
      queue.push(edge.target);
    }
  }
  const reachable = [...depth.keys()].map((id) => byId.get(id)!);
  const sinks = reachable.filter((n) => !edges.some((e) => e.source === n.id && byId.has(e.target)));
  if (sinks.length === 0) return rightmost(reachable);
  return sinks.reduce((best, n) => {
    const d = depth.get(n.id)!;
    const bestDepth = depth.get(best.id)!;
    return d > bestDepth || (d === bestDepth && n.position.x > best.position.x) ? n : best;
  });
}

export interface ResolveAnchorArgs {
  /** The anchor a "+" asked for, if any. */
  pending: InsertAnchor | null;
  nodes: readonly Node[];
  edges: readonly Edge[];
  /** The builder's selected node (the one whose config panel is open). */
  selectedNodeId: string | null;
}

/**
 * Where the next step goes: the "+" that asked for it, else after the single
 * selected node, else after the end of the main path. Null only for a canvas
 * with no nodes.
 */
export function resolveAnchor({ pending, nodes, edges, selectedNodeId }: ResolveAnchorArgs): InsertAnchor | null {
  if (pending && anchorIsValid(pending, nodes, edges)) return pending;
  // React Flow's own selection first: keyboard selection (Enter on a focused
  // node) updates it without going through the builder's click handler.
  const flowSelected = nodes.filter((n) => n.selected);
  const selected =
    flowSelected.length === 1
      ? flowSelected[0]
      : flowSelected.length === 0 && selectedNodeId
        ? nodes.find((n) => n.id === selectedNodeId)
        : undefined;
  const anchorNode = selected ?? mainPathTail(nodes, edges);
  if (!anchorNode) return null;
  return { kind: "after", nodeId: anchorNode.id, sourceHandle: defaultSourceHandle(anchorNode, edges) };
}

interface Rect extends XYPosition, Size {}

function rectOf(node: Node): Rect {
  return { ...node.position, ...nodeSize(node) };
}

function overlaps(a: Rect, b: Rect, margin: number): boolean {
  return a.x < b.x + b.width + margin && b.x < a.x + a.width + margin && a.y < b.y + b.height + margin && b.y < a.y + a.height + margin;
}

/** Move `position` down, past whatever it would cover, until it is clear. */
export function clearPosition(position: XYPosition, size: Size, others: readonly Node[]): XYPosition {
  const next = { ...position };
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const rect = { ...next, ...size };
    const blocking = others.map(rectOf).filter((other) => overlaps(rect, other, INSERT_GAP_Y / 2));
    if (blocking.length === 0) return next;
    next.y = Math.max(...blocking.map((other) => other.y + other.height)) + INSERT_GAP_Y;
  }
  return next;
}

/** The y of the middle of `node`'s output `handle`: a Switch case row, or the node's middle. */
function outputCenterY(node: Node, handle: string | null | undefined): number {
  const index = handle ? switchCases(node).findIndex((c) => c.id === handle) : -1;
  if (index >= 0) {
    return node.position.y + SWITCH_GEOMETRY.HEADER_HEIGHT + index * SWITCH_GEOMETRY.CASE_HEIGHT + SWITCH_GEOMETRY.CASE_HEIGHT / 2;
  }
  return node.position.y + nodeSize(node).height / 2;
}

/** Every node reachable from `startId` without passing through `stopId`. */
function downstreamOf(startId: string, stopId: string, edges: readonly Edge[]): Set<string> {
  const seen = new Set<string>([startId]);
  const queue = [startId];
  while (queue.length > 0) {
    const id = queue.shift()!;
    for (const edge of edges) {
      if (edge.source !== id || edge.target === stopId || seen.has(edge.target)) continue;
      seen.add(edge.target);
      queue.push(edge.target);
    }
  }
  return seen;
}

export function newEdgeId(): string {
  return `edge-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
}

/**
 * An edge in the builder's shape: `custom`, the Switch case as its source
 * handle, and the start node's event as `sourceEvent` (which is how
 * nodesEdgesToWorkflow tells a `started` edge apart).
 */
export function buildFlowEdge(source: Node, targetId: string, sourceHandle?: string | null, id: string = newEdgeId()): Edge {
  const eventType = (source.data as { eventType?: string } | undefined)?.eventType;
  return {
    id,
    source: source.id,
    target: targetId,
    sourceHandle: sourceHandle || undefined,
    type: "custom",
    data: { sourceEvent: isEntryFlowNodeType(source.type) && eventType ? eventType : undefined },
  };
}

/** Edge data that belongs to the route, not to how one edge was last drawn. */
function routeData(data: Edge["data"]): Edge["data"] {
  if (!data) return data;
  const { siblingIndex: _siblingIndex, totalSiblings: _totalSiblings, executionStatus: _executionStatus, ...route } = data as Record<string, unknown>;
  return route;
}

export interface PlanInsertArgs {
  nodes: readonly Node[];
  edges: readonly Edge[];
  /** The node to add. Its position is replaced. */
  node: Node;
  anchor: InsertAnchor | null;
  /** Where a node goes on a canvas with nothing to attach to. */
  fallbackPosition: XYPosition;
}

export interface InsertPlan {
  nodes: Node[];
  edges: Edge[];
  /** The added node, positioned. */
  inserted: Node;
}

/** The canvas after adding `node` at `anchor`: positioned and wired. */
export function planInsert({ nodes, edges, node, anchor, fallbackPosition }: PlanInsertArgs): InsertPlan {
  const size = nodeSize(node);
  const finish = (allNodes: Node[], allEdges: Edge[], position: XYPosition): InsertPlan => {
    const inserted = { ...node, position: clearPosition(position, size, allNodes) };
    return { nodes: [...allNodes, inserted], edges: allEdges, inserted };
  };

  if (anchor?.kind === "after") {
    const source = nodes.find((n) => n.id === anchor.nodeId);
    if (source) {
      const sourceRect = rectOf(source);
      const handle = anchor.sourceHandle ?? null;
      let y = outputCenterY(source, handle) - size.height / 2;
      // A second branch from the same output goes below the ones already there.
      const siblings = edges
        .filter((e) => leavesThrough(e, source.id, handle))
        .map((e) => nodes.find((n) => n.id === e.target))
        .filter((n): n is Node => !!n && n.position.x > sourceRect.x);
      if (siblings.length > 0) y = Math.max(...siblings.map((n) => n.position.y + nodeSize(n).height)) + INSERT_GAP_Y;
      const position = { x: sourceRect.x + sourceRect.width + INSERT_GAP_X, y };
      return finish([...nodes], [...edges, buildFlowEdge(source, node.id, handle)], position);
    }
  }

  if (anchor?.kind === "splice") {
    const edge = edges.find((e) => e.id === anchor.edgeId);
    const from = edge && nodes.find((n) => n.id === edge.source);
    const to = edge && nodes.find((n) => n.id === edge.target);
    if (edge && from && to) {
      const fromRect = rectOf(from);
      const toRect = rectOf(to);
      const rewired = [
        ...edges.filter((e) => e.id !== edge.id),
        { ...edge, id: newEdgeId(), target: node.id, data: routeData(edge.data), selected: false },
        buildFlowEdge(node, to.id),
      ];
      const centerY = (outputCenterY(from, edge.sourceHandle) + toRect.y + toRect.height / 2) / 2;
      const gapStart = fromRect.x + fromRect.width;
      if (toRect.x < fromRect.x) {
        // A loop-back edge (its end is left of its start): there is no gap
        // between its ends to open, so the new step goes below them.
        const position = { x: (fromRect.x + toRect.x) / 2, y: Math.max(fromRect.y + fromRect.height, toRect.y + toRect.height) + INSERT_GAP_Y };
        return finish([...nodes], rewired, position);
      }
      const room = toRect.x - gapStart;
      const needed = size.width + 2 * INSERT_GAP_X;
      if (room >= needed) {
        return finish([...nodes], rewired, { x: gapStart + (room - size.width) / 2, y: centerY - size.height / 2 });
      }
      // Make room: everything from the edge's end onwards moves right. Nodes
      // reachable only by looping back (left of the end) stay put.
      const shift = needed - room;
      const moving = downstreamOf(to.id, from.id, edges);
      const shifted = nodes.map((n) =>
        moving.has(n.id) && n.position.x >= toRect.x - 1 ? { ...n, position: { x: n.position.x + shift, y: n.position.y } } : n,
      );
      return finish(shifted, rewired, { x: gapStart + INSERT_GAP_X, y: centerY - size.height / 2 });
    }
  }

  return finish([...nodes], [...edges], fallbackPosition);
}

/** Who a node is to a person: its kind and its id. */
export function describeNode(node: Node, displayName: (stepType: string) => string): string {
  if (isEntryFlowNodeType(node.type)) return "the start";
  if (node.type === SWITCH_NODE_TYPE) return `Switch (${node.id})`;
  const stepType = (node.data as { step?: { type?: string } } | undefined)?.step?.type;
  return stepType ? `${displayName(stepType)} (${node.id})` : node.id;
}

/**
 * Nodes with an accessible name: "Call LLM, think", plus ", not connected"
 * when nothing leads into it (a step nothing reaches never runs). React Flow
 * makes nodes focusable but leaves them unnamed, so a keyboard or screen
 * reader user tabbing through the canvas heard only "group". Returns the same
 * array when no label changes.
 */
export function withNodeAriaLabels(nodes: Node[], edges: readonly Edge[], displayName: (stepType: string) => string): Node[] {
  const reached = new Set(edges.map((e) => e.target));
  let changed = false;
  const next = nodes.map((node) => {
    const name = describeNode(node, displayName).replace(/ \((.*)\)$/, ", $1");
    const ariaLabel = isEntryFlowNodeType(node.type) ? "Start" : reached.has(node.id) ? name : `${name}, not connected`;
    if (node.ariaLabel === ariaLabel) return node;
    changed = true;
    return { ...node, ariaLabel };
  });
  return changed ? next : nodes;
}

/**
 * A Switch case as a person reads it: its label, its condition, or — with
 * neither, as a new Switch's cases are — "default" for the last and its
 * number for the rest, so no two "+" share a name.
 */
export function describeCase(node: Node, handle: string | null | undefined): string | undefined {
  const cases = switchCases(node);
  const index = handle ? cases.findIndex((c) => c.id === handle) : -1;
  if (index < 0) return undefined;
  const caseItem = cases[index]!;
  return caseItem.label || caseItem.condition || (index === cases.length - 1 ? "default" : `${index + 1}`);
}

/**
 * Two selected nodes in the order an edge between them runs: the start node
 * first, else whichever is earlier on the canvas (left, then top).
 */
export function connectionOrder(a: Node, b: Node): [Node, Node] {
  if (isEntryFlowNodeType(a.type)) return [a, b];
  if (isEntryFlowNodeType(b.type)) return [b, a];
  if (a.position.x !== b.position.x) return a.position.x < b.position.x ? [a, b] : [b, a];
  return a.position.y <= b.position.y ? [a, b] : [b, a];
}

/** Why `source` → `target` cannot be drawn, or null when it can. */
export function connectionProblem(source: Node, target: Node, edges: readonly Edge[]): string | null {
  if (source.id === target.id) return "A step can't connect to itself";
  if (isEntryFlowNodeType(target.type)) return "Nothing can run before the start";
  if (edges.some((e) => e.source === source.id && e.target === target.id)) return "Already connected";
  return null;
}
