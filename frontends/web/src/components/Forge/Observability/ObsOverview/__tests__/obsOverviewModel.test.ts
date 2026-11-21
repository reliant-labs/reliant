// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { GraphEdge, GraphNode, RPCCatalog, RPCMethod, ServiceGraph } from "@/services/controlPlane/observability/graphTypes";

import { graphThumbnail, slowestEndpoints, THUMB_H, THUMB_W } from "../obsOverviewModel";

const BOUNDS = [0.01, 0.1, 1];
/** All `count` calls in one bucket: p95 interpolates inside it. */
const hist = (bucket: number, count: number) => ({
  version: 1,
  boundsSeconds: BOUNDS,
  buckets: BOUNDS.concat(Infinity).map((_, i) => (i === bucket ? BigInt(count) : 0n)),
  count: BigInt(count),
  sumSeconds: 1,
});

const method = (id: string, count: number, bucket: number | null, faults = 0): RPCMethod =>
  ({
    id,
    service: id.split("/")[0]!,
    method: id.split("/")[1]!,
    evidence: "observed",
    observedCount: BigInt(count),
    faults: BigInt(faults),
    duration: bucket === null ? undefined : hist(bucket, count),
  }) as unknown as RPCMethod;

const catalog = (methods: RPCMethod[]): RPCCatalog => ({ methods, declaredCatalogs: [], quality: null, declaredStatus: "", unavailableReason: "" });

describe("slowestEndpoints", () => {
  it("ranks by p95, slowest first, and leaves out a method with no histogram or too few calls", () => {
    const rows = slowestEndpoints(
      catalog([method("a.v1.S/Fast", 100, 0), method("a.v1.S/Slow", 100, 2, 3), method("a.v1.S/Mid", 100, 1), method("a.v1.S/NoHist", 100, null), method("a.v1.S/Rare", 2, 2)])
    );
    expect(rows.map((r) => r.method)).toEqual(["Slow", "Mid", "Fast"]);
    expect(rows[0]!.p95Ms).toBeGreaterThan(100);
    expect(rows[0]!.errorRate).toBeCloseTo(0.03);
  });

  it("is empty with no catalog", () => {
    expect(slowestEndpoints(undefined)).toEqual([]);
  });
});

describe("graphThumbnail", () => {
  const node = (id: string, kind: GraphNode["kind"]): GraphNode => ({ id, envId: "e", kind, label: id, parentId: "", rpcId: "", sourceUnavailableReason: "" });
  const edge = (source: string, target: string, count: number, faults: number): GraphEdge =>
    ({ id: `${source}-${target}`, source, target, envId: "e", kind: "sync", evidence: "observed", rpcId: "", unit: "server_request", viewpoint: "server", cohort: "c", count: BigInt(count), faults: BigInt(faults), userOutcomes: 0n, cancelled: 0n, unknownStatus: 0n, matched: 0n, unmatched: 0n, exemplars: [], firstSeenMs: 0, lastSeenMs: 0 }) as GraphEdge;
  const graph: ServiceGraph = {
    nodes: [node("web", "frontend"), node("api", "process"), node("db", "database")],
    edges: [edge("web", "api", 100, 5), edge("api", "db", 100, 0)],
    quality: null,
    window: null,
  };

  it("fits every node in the frame and marks a failing link", () => {
    const thumb = graphThumbnail(graph);
    expect(thumb.nodes).toHaveLength(3);
    for (const n of thumb.nodes) {
      expect(n.x).toBeGreaterThanOrEqual(0);
      expect(n.x).toBeLessThanOrEqual(THUMB_W);
      expect(n.y).toBeGreaterThanOrEqual(0);
      expect(n.y).toBeLessThanOrEqual(THUMB_H);
    }
    expect(thumb.links.find((l) => l.id.startsWith("web"))?.failing).toBe(true);
    expect(thumb.links.find((l) => l.id.startsWith("api"))?.failing).toBe(false);
  });

  it("is empty with no graph", () => {
    expect(graphThumbnail(undefined)).toEqual({ nodes: [], links: [] });
  });
});
