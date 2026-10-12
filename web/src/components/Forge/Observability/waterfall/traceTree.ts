/**
 * Span-tree construction and collapse logic for the trace waterfall.
 *
 * Ported from HyperDX's DBTraceWaterfallChart.tsx
 * (https://github.com/hyperdxio/hyperdx, packages/app/src/components/
 * DBTraceWaterfallChart.tsx), adapted to Reliant's Span model: the
 * placeholder-parent DAG build, the multi-root and missing-parent edge cases,
 * the per-level expand/collapse operations and the trace-extent computation
 * are HyperDX's; the types, naming and the absence of correlated log rows are
 * ours. Attribution is also recorded in LICENSES.txt at the repository root.
 *
 * ---------------------------------------------------------------------------
 * MIT License
 *
 * Copyright (c) 2023 DeploySentinel, Inc.
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 * ---------------------------------------------------------------------------
 */

import type { Span } from "@/services/controlPlane/observability/types";

export interface SpanNode {
  /** Unique per row — a span id can repeat (a retried export), so this is the row's own key. */
  id: string;
  span: Span;
  children: SpanNode[];
}

export interface FlatSpan {
  node: SpanNode;
  level: number;
}

export interface TraceTree {
  roots: SpanNode[];
  /** Every real node by id (placeholders excluded). */
  nodesById: Map<string, SpanNode>;
  /** level → ids of the nodes at that level that have children. Drives the per-level controls. */
  parentIdsByLevel: Map<number, Set<string>>;
}

/**
 * Build the span DAG. Three edge cases, as HyperDX names them:
 *   1. spans whose parent was never fetched (sampling, a window edge) — roots;
 *   2. a parent that arrives AFTER its children in start order — a placeholder
 *      collects the children until the parent is seen, then hands them over;
 *   3. several roots in one trace (disjoint front-end and back-end halves).
 * A repeated span id keeps the FIRST occurrence as the one children attach to.
 */
export function buildTraceTree(spans: Span[]): TraceTree {
  const ordered = [...spans].sort((a, b) => a.startMs - b.startMs);
  const validSpanIds = new Set(ordered.filter((s) => s.spanId).map((s) => s.spanId));

  const roots: SpanNode[] = [];
  const nodesById = new Map<string, SpanNode>();
  const placeholders = new Map<string, SpanNode[]>();
  const canonicalBySpanId = new Map<string, SpanNode>();

  ordered.forEach((span, index) => {
    if (!span.spanId) return;
    const node: SpanNode = { id: `${span.spanId}:${index}`, span, children: [] };

    if (!canonicalBySpanId.has(span.spanId)) {
      canonicalBySpanId.set(span.spanId, node);
      const waiting = placeholders.get(span.spanId);
      if (waiting) {
        node.children = waiting;
        placeholders.delete(span.spanId);
      }
    }
    nodesById.set(node.id, node);

    const parentId = span.parentSpanId;
    if (!parentId || !validSpanIds.has(parentId) || parentId === span.spanId) {
      roots.push(node);
      return;
    }
    const parent = canonicalBySpanId.get(parentId);
    if (parent) {
      parent.children.push(node);
    } else {
      const waiting = placeholders.get(parentId) ?? [];
      waiting.push(node);
      placeholders.set(parentId, waiting);
    }
  });

  // Reliant addition: a parent cycle (A's parent is B, B's parent is A — a
  // buggy producer) leaves those spans reachable from no root. Promote the
  // earliest unreached span of each cycle to a root so nothing disappears;
  // every traversal below carries a visited set, so the cycle cannot loop.
  const reached = new Set<string>();
  const mark = (node: SpanNode) => {
    if (reached.has(node.id)) return;
    reached.add(node.id);
    node.children.forEach(mark);
  };
  roots.forEach(mark);
  for (const node of nodesById.values()) {
    if (!reached.has(node.id)) {
      roots.push(node);
      mark(node);
    }
  }

  // Children in start order — the placeholder hand-off can leave them out of it.
  const sorted = new Set<string>();
  const sortChildren = (node: SpanNode) => {
    if (sorted.has(node.id)) return;
    sorted.add(node.id);
    node.children.sort((a, b) => a.span.startMs - b.span.startMs);
    node.children.forEach(sortChildren);
  };
  roots.forEach(sortChildren);

  const parentIdsByLevel = new Map<number, Set<string>>();
  const collected = new Set<string>();
  const collectParents = (node: SpanNode, level: number) => {
    if (collected.has(node.id)) return;
    collected.add(node.id);
    if (node.children.length > 0) {
      if (!parentIdsByLevel.has(level)) parentIdsByLevel.set(level, new Set());
      parentIdsByLevel.get(level)!.add(node.id);
    }
    node.children.forEach((child) => collectParents(child, level + 1));
  };
  roots.forEach((root) => collectParents(root, 0));

  return { roots, nodesById, parentIdsByLevel };
}

/** Depth-first flattening, skipping the subtrees of collapsed nodes. Each node appears once. */
export function flattenTree(roots: SpanNode[], collapsed: Set<string>): FlatSpan[] {
  const out: FlatSpan[] = [];
  const seen = new Set<string>();
  const visit = (node: SpanNode, level: number) => {
    if (seen.has(node.id)) return;
    seen.add(node.id);
    out.push({ node, level });
    if (collapsed.has(node.id)) return;
    node.children.forEach((child) => visit(child, level + 1));
  };
  roots.forEach((root) => visit(root, 0));
  return out;
}

export function getDescendantIds(node: SpanNode, seen: Set<string> = new Set([node.id])): string[] {
  const ids: string[] = [];
  for (const child of node.children) {
    if (seen.has(child.id)) continue;
    seen.add(child.id);
    ids.push(child.id, ...getDescendantIds(child, seen));
  }
  return ids;
}

/** "Collapse all": every collapsible parent, at every level. */
export function computeCollapseAll(parentIdsByLevel: Map<number, Set<string>>): Set<string> {
  const all = new Set<string>();
  parentIdsByLevel.forEach((ids) => ids.forEach((id) => all.add(id)));
  return all;
}

/** "Expand one level": the shallowest level that still has a collapsed parent. */
export function computeExpandOneLevel(collapsed: Set<string>, parentIdsByLevel: Map<number, Set<string>>): Set<string> {
  if (collapsed.size === 0) return collapsed;
  const next = new Set(collapsed);
  for (const level of [...parentIdsByLevel.keys()].sort((a, b) => a - b)) {
    const atLevel = [...parentIdsByLevel.get(level)!].filter((id) => next.has(id));
    if (atLevel.length > 0) {
      atLevel.forEach((id) => next.delete(id));
      break;
    }
  }
  return next;
}

/** "Collapse one level": the deepest level that still has an expanded parent. */
export function computeCollapseOneLevel(collapsed: Set<string>, parentIdsByLevel: Map<number, Set<string>>): Set<string> {
  const next = new Set(collapsed);
  for (const level of [...parentIdsByLevel.keys()].sort((a, b) => b - a)) {
    const atLevel = [...parentIdsByLevel.get(level)!].filter((id) => !next.has(id));
    if (atLevel.length > 0) {
      atLevel.forEach((id) => next.add(id));
      break;
    }
  }
  return next;
}

/** Toggle one node; with `includeDescendants` (Alt-click) its whole subtree follows. */
export function computeToggleCollapse(
  collapsed: Set<string>,
  id: string,
  node: SpanNode | undefined,
  includeDescendants: boolean
): Set<string> {
  const next = new Set(collapsed);
  const wasCollapsed = next.has(id);
  if (wasCollapsed) next.delete(id);
  else next.add(id);
  if (includeDescendants && node?.children.length) {
    for (const descendant of getDescendantIds(node)) {
      if (wasCollapsed) next.delete(descendant);
      else next.add(descendant);
    }
  }
  return next;
}

export interface TraceExtent {
  startMs: number;
  /** Wall clock from the earliest span start to the latest span end. */
  totalDurationMs: number;
  /** The earliest span's parent was never fetched — the trace began before what we have. */
  startClipped: boolean;
}

export function traceExtent(spans: Span[]): TraceExtent | null {
  const validSpanIds = new Set(spans.map((s) => s.spanId));
  let start = Number.POSITIVE_INFINITY;
  let end = Number.NEGATIVE_INFINITY;
  let startClipped = false;
  for (const span of spans) {
    if (!Number.isFinite(span.startMs)) continue;
    if (span.startMs < start) {
      start = span.startMs;
      startClipped = !!span.parentSpanId && !validSpanIds.has(span.parentSpanId);
    }
    end = Math.max(end, span.startMs + (Number.isFinite(span.durationMs) ? span.durationMs : 0));
  }
  if (!Number.isFinite(start)) return null;
  return { startMs: start, totalDurationMs: end - start, startClipped };
}

/**
 * A stable colour slot per service: names sorted, so a service keeps its
 * colour however the rows are ordered. The slot indexes a token palette in the
 * renderer; red is reserved for error spans and never handed to a service.
 */
export function serviceSlots(spans: Span[]): Map<string, number> {
  const names = [...new Set(spans.map((s) => s.service).filter(Boolean))].sort();
  return new Map(names.map((name, index) => [name, index]));
}
