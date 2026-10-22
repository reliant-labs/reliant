// Copyright (c) 2025 Reliant Labs

/** The span-tree build ported from HyperDX, and the edge cases it exists for. */

import { describe, expect, it } from "vitest";

import type { Span } from "@/services/controlPlane/observability/types";

import {
  buildTraceTree,
  computeCollapseAll,
  computeCollapseOneLevel,
  computeExpandOneLevel,
  computeToggleCollapse,
  flattenTree,
  traceExtent,
} from "../waterfall/traceTree";

function span(spanId: string, parentSpanId: string, startMs: number, durationMs = 10): Span {
  return {
    spanId,
    parentSpanId,
    name: spanId,
    service: "svc",
    kind: "internal",
    startMs,
    durationMs,
    status: "ok",
    statusMessage: "",
    attributes: {},
    resource: {},
    events: [],
    source: "",
    operatorData: false,
  };
}

const names = (spans: Span[], collapsed = new Set<string>()) =>
  flattenTree(buildTraceTree(spans).roots, collapsed).map(({ node, level }) => `${"  ".repeat(level)}${node.span.spanId}`);

describe("buildTraceTree", () => {
  it("nests children under parents, in start order", () => {
    expect(names([span("c", "a", 5), span("a", "", 0, 50), span("b", "a", 2)])).toEqual(["a", "  b", "  c"]);
  });

  it("hands children seen before their parent over from a placeholder (clock skew)", () => {
    // The child starts BEFORE its parent: a producer with a skewed clock.
    expect(names([span("child", "parent", 0), span("parent", "", 1, 20)])).toEqual(["parent", "  child"]);
  });

  it("makes a span whose parent was never fetched a root", () => {
    expect(names([span("a", "", 0), span("orphan", "sampled-away", 3)])).toEqual(["a", "orphan"]);
  });

  it("keeps several roots (disjoint halves of one trace)", () => {
    expect(names([span("fe", "", 0), span("be", "", 1), span("be-child", "be", 2)])).toEqual(["fe", "be", "  be-child"]);
  });

  it("survives a parent cycle without losing or repeating a span", () => {
    const flat = names([span("x", "y", 0), span("y", "x", 1)]);
    expect(flat.map((n) => n.trim()).sort()).toEqual(["x", "y"]);
  });
});

describe("collapse operations", () => {
  const spans = [span("r", "", 0, 100), span("a", "r", 1), span("a1", "a", 2), span("b", "r", 3), span("b1", "b", 4)];
  const tree = buildTraceTree(spans);
  const id = (spanId: string) => [...tree.nodesById.values()].find((n) => n.span.spanId === spanId)!.id;

  it("collapses a node's subtree out of the flat list", () => {
    expect(names(spans, new Set([id("a")]))).toEqual(["r", "  a", "  b", "    b1"]);
  });

  it("collapses and expands one level at a time", () => {
    const deepest = computeCollapseOneLevel(new Set(), tree.parentIdsByLevel);
    expect([...deepest].sort()).toEqual([id("a"), id("b")].sort());
    const all = computeCollapseAll(tree.parentIdsByLevel);
    expect(all.size).toBe(3);
    const oneOpen = computeExpandOneLevel(all, tree.parentIdsByLevel);
    expect(oneOpen.has(id("r"))).toBe(false);
    expect(oneOpen.has(id("a"))).toBe(true);
  });

  it("toggles a whole subtree with Alt", () => {
    const next = computeToggleCollapse(new Set(), id("r"), tree.nodesById.get(id("r")), true);
    expect(next.size).toBe(spans.length);
  });
});

describe("traceExtent", () => {
  it("is the wall clock from the first start to the last end, flagged when the start is clipped", () => {
    expect(traceExtent([span("a", "", 100, 50), span("b", "a", 120, 60)])).toEqual({
      startMs: 100,
      totalDurationMs: 80,
      startClipped: false,
    });
    expect(traceExtent([span("a", "missing", 100, 50)])?.startClipped).toBe(true);
  });
});
