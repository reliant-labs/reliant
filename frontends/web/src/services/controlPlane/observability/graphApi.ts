// Copyright (c) 2026 Reliant Labs
import {
  GraphNodeKind, GraphEdgeKind, GraphEvidenceKind, ObservabilityService,
  type GraphCompleteness, type GraphHistogram,
} from "@/gen/controlplane/services/observability/v1/observability_pb";
import { obsClient } from "./obsClient";
import { msOf, wireScope } from "./api";
import type { CatalogRequest, GraphEdge, GraphNode, GraphQuality, GraphRequest, RPCCatalog, ServiceGraph } from "./graphTypes";

const NODE_KIND: Partial<Record<GraphNodeKind, GraphNode["kind"]>> = {
  [GraphNodeKind.FRONTEND]: "frontend", [GraphNodeKind.PROCESS]: "process", [GraphNodeKind.RPC]: "rpc", [GraphNodeKind.INTERNAL_METHOD]: "method",
  [GraphNodeKind.DATABASE]: "database", [GraphNodeKind.EXTERNAL]: "external", [GraphNodeKind.BROKER]: "broker",
  [GraphNodeKind.WORKFLOW]: "workflow",
};
const EDGE_KIND: Partial<Record<GraphEdgeKind, GraphEdge["kind"]>> = {
  [GraphEdgeKind.SYNC]: "sync", [GraphEdgeKind.DATABASE]: "database", [GraphEdgeKind.EXTERNAL]: "external",
  [GraphEdgeKind.ASYNC_SEND]: "async-send", [GraphEdgeKind.ASYNC_CONSUME]: "async-consume",
  [GraphEdgeKind.INTERNAL]: "internal", [GraphEdgeKind.DECLARED]: "declared",
};
export const graphEvidence = (kind: GraphEvidenceKind): GraphEdge["evidence"] =>
  kind === GraphEvidenceKind.OBSERVED ? "observed" : kind === GraphEvidenceKind.DECLARED ? "declared" : "unknown";

function histogram(h?: GraphHistogram): GraphEdge["duration"] {
  return h ? { version: h.version, boundsSeconds: h.upperBoundsSeconds, buckets: h.bucketCounts, count: h.count, sumSeconds: h.sumSeconds } : undefined;
}

function quality(q?: GraphCompleteness): GraphQuality | null {
  return q ? {
    origin: q.metricsOrigin, lossUnknown: q.upstreamLossUnknown, samplingUnknown: q.upstreamSamplingUnknown,
    truncated: q.truncated, provisional: q.provisional, duplicates: q.duplicateCount, conflicts: q.conflictingCount,
    unmatched: q.unmatchedCount, rejected: q.rejectedCount, overflow: q.overflowCount,
    aggregatedThroughMs: msOf(q.aggregatedThrough), warnings: q.warnings,
    availableMinutes: q.availableMinutes, requestedMinutes: q.requestedMinutes,
  } : null;
}

export async function getServiceGraph(req: GraphRequest, options: { signal?: AbortSignal } = {}): Promise<ServiceGraph> {
  const res = await obsClient(ObservabilityService).getServiceGraph({
    scope: wireScope(req, req.window, req.services), rpcIds: req.rpcIds, rpcService: req.rpcService ?? "", includeInternal: req.includeInternal, maxEdges: 500,
    edgeKinds: [GraphEdgeKind.SYNC, GraphEdgeKind.DATABASE, GraphEdgeKind.EXTERNAL, GraphEdgeKind.ASYNC_SEND,
      GraphEdgeKind.ASYNC_CONSUME, ...(req.includeInternal ? [GraphEdgeKind.INTERNAL] : []), ...(req.includeDeclared ? [GraphEdgeKind.DECLARED] : [])],
  }, options);
  return {
    nodes: res.nodes.map((n) => ({ id: n.id, envId: n.envId, kind: NODE_KIND[n.kind] ?? "unknown", label: n.label,
      parentId: n.parentNodeId, rpcId: n.rpcId, sourceUnavailableReason: n.sourceUnavailableReason })),
    edges: res.edges.map((e) => ({ id: e.id, source: e.sourceNodeId, target: e.destinationNodeId, envId: e.envId,
      kind: EDGE_KIND[e.kind] ?? "unknown", evidence: graphEvidence(e.evidence), rpcId: e.rpcId, unit: e.unit,
      viewpoint: e.viewpoint, cohort: e.cohort, count: e.count, faults: e.serverFaultCount,
      userOutcomes: e.userOutcomeCount, cancelled: e.cancelledCount, unknownStatus: e.unknownStatusCount,
      matched: e.matchedCount, unmatched: e.unmatchedCount,
      duration: histogram(e.duration),
      exemplars: e.exemplars.map((x) => ({ traceId: x.traceId, spanId: x.spanId, availabilityUnknown: x.traceAvailabilityUnknown })),
      firstSeenMs: msOf(e.firstSeen), lastSeenMs: msOf(e.lastSeen),
    })),
    quality: quality(res.completeness),
    window: res.effectiveTimeRange ? { startMs: msOf(res.effectiveTimeRange.start), endMs: msOf(res.effectiveTimeRange.end) } : null,
  };
}

export async function getServiceGraphCatalog(req: CatalogRequest, options: { signal?: AbortSignal } = {}): Promise<RPCCatalog> {
  const res = await obsClient(ObservabilityService).getServiceGraphCatalog({
    scope: wireScope(req, req.window, req.services), rpcService: req.rpcService ?? "", rpcIds: req.rpcIds ?? [], maxMethods: 500,
  }, options);
  return {
    methods: res.methods.map((m) => ({ id: m.rpcId, service: m.rpcService, method: m.method,
      processIds: m.processNodeIds, evidence: graphEvidence(m.evidence), observedCount: m.observedCount,
      faults: m.serverFaultCount, userOutcomes: m.userOutcomeCount, duration: histogram(m.duration),
      inputType: m.inputType, outputType: m.outputType, streaming: m.streamMode, sourceUnavailableReason: m.sourceUnavailableReason,
      idempotency: m.idempotency, authRequired: m.authRequired, servesProcessIds: m.servesProcessIds, canCallProcessIds: m.canCallProcessIds })),
    declaredCatalogs: res.declaredCatalogs.map((c) => ({ envId: c.envId, releaseId: c.releaseId,
      bundleDigest: c.bundleDigest, json: c.catalogJson, repository: c.repository, commit: c.commit })),
    quality: quality(res.completeness), declaredStatus: res.declaredCatalogStatus, unavailableReason: res.declaredCatalogUnavailableReason,
  };
}
