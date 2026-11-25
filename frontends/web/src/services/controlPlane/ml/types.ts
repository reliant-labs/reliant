// Copyright (c) 2026 Reliant Labs

/**
 * The plain types the ML tab (components/Forge/Ml) speaks: an org's
 * accelerator machines, the ML stack found in its clusters, recommendations,
 * training runs, model versions and inference services, as control-plane's
 * `controlplane.v1.MlEarlyAccessService` reports them (control-plane
 * proto/services/ml_early_access/v1/ml_early_access.proto, whose header is the
 * behaviour spec; design: forge docs/proposals/ml-workloads.md §8). Times are
 * epoch ms (null when the server sent none), enums are names, and nothing here
 * knows the wire: wire.ts maps it.
 *
 * EARLY ACCESS, BY REQUEST. Every read answers one of three ways (MlOutcome):
 * the data; "not entitled" (the org has no "ml" entitlement, or lost it); or
 * "no data yet" (entitled, and this control plane does not hold that data
 * yet). The second and third are states the pages draw, never errors, and
 * neither is ever filled in with invented rows.
 */

/** Per-call options the client forwards to Connect (a query's abort signal). */
export interface CallOptions {
  signal?: AbortSignal;
}

/** One ML read's answer: the data, or why there is none. Every other failure throws. */
export type MlOutcome<T> =
  | { kind: "ok"; data: T }
  /** PermissionDenied, reason FEATURE_NOT_ENTITLED: the org has no "ml" entitlement. */
  | { kind: "not-entitled" }
  /** Unimplemented: the org is entitled, and this control plane has no such data yet. */
  | { kind: "no-data" };

// ── Entitlements (OrganizationService) ──────────────────────────────────────

/** The one feature granted by request today. */
export const ML_FEATURE = "ml";

/** What a member may know of one feature granted by request. */
export interface OrgEntitlement {
  feature: string;
  enabled: boolean;
  /** When someone in the org last asked for it; null when nobody has. */
  requestedAtMs: number | null;
  /** When it was granted; null unless enabled. */
  grantedAtMs: number | null;
}

/** RequestOrgEntitlement's answer: the feature's state after the call. */
export interface EntitlementRequestResult {
  entitlement: OrgEntitlement;
  /** It had been asked for already; nothing was recorded again. */
  alreadyRequested: boolean;
  /** The org has it already; nothing was recorded. */
  alreadyEnabled: boolean;
}

// ── Machines ────────────────────────────────────────────────────────────────

export interface MlCluster {
  id: string;
  name: string;
  /** "gke", "eks", "aks" or "other". */
  provider: string;
  location: string;
  lastSeenAtMs: number | null;
}

export type MlNodeHealth = "healthy" | "degraded" | "bad" | "unknown";

export interface MlHealthFlag {
  /** "xid", "ecc_dbe", "row_remap_failure", "condition" or "allocatable_below_capacity". */
  kind: string;
  code: string;
  /** e.g. "GPU-3"; empty for the node as a whole. */
  device: string;
  message: string;
  firstSeenAtMs: number | null;
  lastSeenAtMs: number | null;
}

export interface MlNode {
  name: string;
  acceleratorCount: number;
  acceleratorsAllocatable: number;
  acceleratorsAllocated: number;
  /** 0-100, the last 5 minutes. */
  utilizationPercent: number;
  memoryUsedPercent: number;
  driverVersion: string;
  /** "none", "mig" or "time-slicing". */
  sharing: string;
  health: MlNodeHealth;
  flags: MlHealthFlag[];
  cordoned: boolean;
}

export interface MlUtilizationPoint {
  atMs: number;
  /** 0-100. */
  utilizationPercent: number;
  /** 0-100. */
  allocatedPercent: number;
}

export interface MlNodePool {
  clusterId: string;
  name: string;
  acceleratorVendor: string;
  acceleratorProduct: string;
  acceleratorsTotal: number;
  acceleratorsAllocated: number;
  /** Allocated, but under 5% utilisation for at least an hour. */
  acceleratorsIdle: number;
  nodes: MlNode[];
  /** The last 24h, oldest first; points without a time are dropped. */
  utilization: MlUtilizationPoint[];
}

export interface MlMachines {
  clusters: MlCluster[];
  pools: MlNodePool[];
  observedAtMs: number | null;
}

// ── Stack ───────────────────────────────────────────────────────────────────

export type MlComponentState = "adopted" | "installed" | "missing" | "unknown";

export interface MlStackComponent {
  clusterId: string;
  name: string;
  category: string;
  state: MlComponentState;
  version: string;
  via: string;
  namespace: string;
  /** The KCL that declares it, as `forge ml discover` proposes it. */
  declaration: string;
  note: string;
}

// ── Recommendations ─────────────────────────────────────────────────────────

export type MlFindingSeverity = "critical" | "warning" | "info" | "unknown";

export interface MlStartStage {
  name: string;
  p50Seconds: number;
  p95Seconds: number;
}

export interface MlPodStartBreakdown {
  workload: string;
  sampleCount: number;
  stages: MlStartStage[];
}

export interface MlFinding {
  id: string;
  ruleId: string;
  severity: MlFindingSeverity;
  title: string;
  summary: string;
  clusterId: string;
  subject: string;
  evidence: { label: string; value: string }[];
  fixKcl: string;
  fixCommand: string;
  firstSeenAtMs: number | null;
  lastSeenAtMs: number | null;
  podStart: MlPodStartBreakdown | null;
}

// ── Runs ────────────────────────────────────────────────────────────────────

export type MlRunState = "queued" | "running" | "succeeded" | "failed" | "cancelled" | "unknown";

export interface MlMetricSeries {
  name: string;
  unit: string;
  points: { step: number; atMs: number | null; value: number }[];
}

export interface MlRun {
  id: string;
  name: string;
  project: string;
  template: string;
  orchestrator: string;
  clusterId: string;
  state: MlRunState;
  params: Record<string, string>;
  launchedBy: string;
  startedAtMs: number | null;
  finishedAtMs: number | null;
  acceleratorCount: number;
  acceleratorProduct: string;
  restarts: number;
  preemptions: number;
  metrics: MlMetricSeries[];
  outputModelDigest: string;
}

// ── Models ──────────────────────────────────────────────────────────────────

export interface MlModelVersion {
  digest: string;
  tag: string;
  createdAtMs: number | null;
  runId: string;
  sizeBytes: number;
  evalMetrics: Record<string, number>;
  /** "<project>/<env>/<service>". */
  servingIn: string[];
}

export interface MlModel {
  name: string;
  project: string;
  repository: string;
  /** Newest first. */
  versions: MlModelVersion[];
}

// ── Inference ───────────────────────────────────────────────────────────────

export type MlReleaseVerdict = "pass" | "fail" | "inconclusive" | "unknown";

export interface MlInferenceService {
  name: string;
  project: string;
  environment: string;
  clusterId: string;
  runtime: string;
  release: string;
  modelDigest: string;
  replicas: number;
  ttftP95Ms: number;
  queueWaitP95Ms: number;
  tokensPerSecond: number;
  acceleratorUtilizationPercent: number;
  kvCacheUsagePercent: number;
  coldStart: MlPodStartBreakdown | null;
  releaseVerdict: MlReleaseVerdict;
  releaseVerdictSummary: string;
  releaseCheckedAtMs: number | null;
}

// ── Requests ────────────────────────────────────────────────────────────────

/** Machines, Stack and Recommendations: one org, optionally one cluster. */
export interface MlClusterRequest {
  orgId: string;
  clusterId?: string;
}

/** Runs, Models and Inference: one org, optionally one project (app). */
export interface MlProjectRequest {
  orgId: string;
  project?: string;
}
