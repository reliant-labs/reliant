// Copyright (c) 2026 Reliant Labs
import type { ObsScope, TimeWindow } from "./types";

/** Presentation types, not a second wire contract. Counters stay exact. */
export type GraphNodeKind = "process" | "frontend" | "api" | "service" | "rpc" | "method" | "database" | "external" | "broker" | "workflow" | "unknown";
export interface GraphNode {
  id: string;
  envId: string;
  kind: GraphNodeKind;
  label: string;
  parentId: string;
  rpcId: string;
  sourceUnavailableReason: string;
}
export interface GraphEdge {
  id: string;
  source: string;
  target: string;
  envId: string;
  kind: "sync" | "database" | "external" | "async-send" | "async-consume" | "internal" | "declared" | "unknown";
  evidence: "observed" | "declared" | "unknown";
  rpcId: string;
  unit: string;
  viewpoint: string;
  cohort: string;
  count: bigint;
  faults: bigint;
  userOutcomes: bigint;
  cancelled: bigint;
  unknownStatus: bigint;
  matched: bigint;
  unmatched: bigint;
  duration?: { version: number; boundsSeconds: number[]; buckets: bigint[]; count: bigint; sumSeconds: number };
  exemplars: { traceId: string; spanId: string; availabilityUnknown: boolean }[];
  firstSeenMs: number;
  lastSeenMs: number;
}
export interface GraphQuality {
  origin: string;
  lossUnknown: boolean;
  samplingUnknown: boolean;
  truncated: boolean;
  provisional: boolean;
  duplicates: bigint;
  conflicts: bigint;
  unmatched: bigint;
  rejected: bigint;
  overflow: bigint;
  aggregatedThroughMs: number;
  warnings: string[];
  availableMinutes: number;
  requestedMinutes: number;
}
export interface ServiceGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
  quality: GraphQuality | null;
  window: TimeWindow | null;
}
export interface RPCMethod {
  id: string;
  service: string;
  method: string;
  processIds: string[];
  evidence: GraphEdge["evidence"];
  observedCount: bigint;
  inputType: string;
  outputType: string;
  streaming: string;
  sourceUnavailableReason: string;
  idempotency: string;
  authRequired?: boolean;
  servesProcessIds: string[];
  canCallProcessIds: string[];
}
export interface RPCCatalog {
  declaredCatalogs: { envId: string; releaseId: string; bundleDigest: string; json: string; repository: string; commit: string }[];
  methods: RPCMethod[];
  quality: GraphQuality | null;
  declaredStatus: string;
  unavailableReason: string;
}
export interface GraphRequest extends ObsScope {
  services?: string[];
  window: TimeWindow;
  rpcIds: string[];
  rpcService?: string;
  includeInternal: boolean;
  includeDeclared?: boolean;
}
export interface CatalogRequest extends ObsScope { window: TimeWindow; rpcService?: string; rpcIds?: string[]; services?: string[] }
