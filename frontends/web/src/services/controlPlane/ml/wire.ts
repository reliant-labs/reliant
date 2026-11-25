// Copyright (c) 2026 Reliant Labs

/**
 * The ML tab's wire mapping: MlEarlyAccessService's and OrganizationService's
 * entitlement messages → the page view-models (types.ts). Pure, so it is
 * tested without a transport.
 *
 * Timestamps become epoch ms (the zero timestamp is "unset", never 1970),
 * int64s become numbers (counts and sizes stay far below 2^53), enums become
 * names, and an unknown enum value (a newer server) becomes "unknown", never
 * a guess.
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  MlComponentState as WireComponentState,
  MlFindingSeverity as WireSeverity,
  MlNodeHealth as WireNodeHealth,
  MlReleaseVerdict as WireVerdict,
  MlRunState as WireRunState,
  type GetMlMachinesResponse,
  type GetMlStackResponse,
  type ListMlFindingsResponse,
  type ListMlInferenceServicesResponse,
  type ListMlModelsResponse,
  type ListMlRunsResponse,
  type MlPodStartBreakdown as WirePodStart,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type {
  GetMyOrgEntitlementsResponse,
  OrgFeatureEntitlement,
  RequestOrgEntitlementResponse,
} from "@/gen/controlplane/services/organization/v1/organization_pb";

import type {
  EntitlementRequestResult,
  MlComponentState,
  MlFinding,
  MlFindingSeverity,
  MlInferenceService,
  MlMachines,
  MlModel,
  MlNodeHealth,
  MlPodStartBreakdown,
  MlReleaseVerdict,
  MlRun,
  MlRunState,
  MlStackComponent,
  OrgEntitlement,
} from "./types";

function ms(ts: Timestamp | undefined): number | null {
  if (!ts) return null;
  const value = timestampMs(ts);
  return value > 0 ? value : null;
}

const n = (value: bigint | undefined) => Number(value ?? 0n);

function nameOf<T extends string>(names: Partial<Record<number, T>>, fallback: T) {
  return (value: number): T => names[value] ?? fallback;
}

export const nodeHealthOf = nameOf<MlNodeHealth>(
  { [WireNodeHealth.HEALTHY]: "healthy", [WireNodeHealth.DEGRADED]: "degraded", [WireNodeHealth.BAD]: "bad" },
  "unknown",
);
export const componentStateOf = nameOf<MlComponentState>(
  { [WireComponentState.ADOPTED]: "adopted", [WireComponentState.INSTALLED]: "installed", [WireComponentState.MISSING]: "missing" },
  "unknown",
);
export const severityOf = nameOf<MlFindingSeverity>(
  { [WireSeverity.CRITICAL]: "critical", [WireSeverity.WARNING]: "warning", [WireSeverity.INFO]: "info" },
  "unknown",
);
export const runStateOf = nameOf<MlRunState>(
  {
    [WireRunState.QUEUED]: "queued",
    [WireRunState.RUNNING]: "running",
    [WireRunState.SUCCEEDED]: "succeeded",
    [WireRunState.FAILED]: "failed",
    [WireRunState.CANCELLED]: "cancelled",
  },
  "unknown",
);
export const verdictOf = nameOf<MlReleaseVerdict>(
  { [WireVerdict.PASS]: "pass", [WireVerdict.FAIL]: "fail", [WireVerdict.INCONCLUSIVE]: "inconclusive" },
  "unknown",
);

// ── Entitlements ────────────────────────────────────────────────────────────

export function entitlementOf(e: OrgFeatureEntitlement): OrgEntitlement {
  return { feature: e.feature, enabled: e.enabled, requestedAtMs: ms(e.requestedAt), grantedAtMs: ms(e.grantedAt) };
}

export function entitlementsOf(res: GetMyOrgEntitlementsResponse): OrgEntitlement[] {
  return res.features.map(entitlementOf);
}

/** RequestOrgEntitlement's answer; a server that sent no entitlement back answers for `feature`, requested now. */
export function requestResultOf(res: RequestOrgEntitlementResponse, feature: string, nowMs: number): EntitlementRequestResult {
  const entitlement = res.entitlement
    ? entitlementOf(res.entitlement)
    : { feature, enabled: res.alreadyEnabled, requestedAtMs: res.alreadyEnabled ? null : nowMs, grantedAtMs: null };
  return { entitlement, alreadyRequested: res.alreadyRequested, alreadyEnabled: res.alreadyEnabled };
}

// ── ML reads ────────────────────────────────────────────────────────────────

function podStartOf(p: WirePodStart | undefined): MlPodStartBreakdown | null {
  if (!p) return null;
  return {
    workload: p.workload,
    sampleCount: p.sampleCount,
    stages: p.stages.map((s) => ({ name: s.name, p50Seconds: s.p50Seconds, p95Seconds: s.p95Seconds })),
  };
}

export function machinesOf(res: GetMlMachinesResponse): MlMachines {
  return {
    clusters: res.clusters.map((c) => ({ id: c.clusterId, name: c.name, provider: c.provider, location: c.location, lastSeenAtMs: ms(c.lastSeenAt) })),
    pools: res.pools.map((p) => ({
      clusterId: p.clusterId,
      name: p.name,
      acceleratorVendor: p.acceleratorVendor,
      acceleratorProduct: p.acceleratorProduct,
      acceleratorsTotal: p.acceleratorsTotal,
      acceleratorsAllocated: p.acceleratorsAllocated,
      acceleratorsIdle: p.acceleratorsIdle,
      nodes: p.nodes.map((node) => ({
        name: node.name,
        acceleratorCount: node.acceleratorCount,
        acceleratorsAllocatable: node.acceleratorsAllocatable,
        acceleratorsAllocated: node.acceleratorsAllocated,
        utilizationPercent: node.utilizationPercent,
        memoryUsedPercent: node.memoryUsedPercent,
        driverVersion: node.driverVersion,
        sharing: node.sharing,
        health: nodeHealthOf(node.health),
        flags: node.flags.map((f) => ({
          kind: f.kind,
          code: f.code,
          device: f.device,
          message: f.message,
          firstSeenAtMs: ms(f.firstSeenAt),
          lastSeenAtMs: ms(f.lastSeenAt),
        })),
        cordoned: node.cordoned,
      })),
      // A point with no time cannot be placed on the 24h axis.
      utilization: p.utilization.flatMap((u) => {
        const atMs = ms(u.time);
        return atMs === null ? [] : [{ atMs, utilizationPercent: u.utilizationPercent, allocatedPercent: u.allocatedPercent }];
      }),
    })),
    observedAtMs: ms(res.observedAt),
  };
}

export function stackOf(res: GetMlStackResponse): MlStackComponent[] {
  return res.components.map((c) => ({
    clusterId: c.clusterId,
    name: c.name,
    category: c.category,
    state: componentStateOf(c.state),
    version: c.version,
    via: c.via,
    namespace: c.namespace,
    declaration: c.declaration,
    note: c.note,
  }));
}

export function findingsOf(res: ListMlFindingsResponse): MlFinding[] {
  return res.findings.map((f) => ({
    id: f.id,
    ruleId: f.ruleId,
    severity: severityOf(f.severity),
    title: f.title,
    summary: f.summary,
    clusterId: f.clusterId,
    subject: f.subject,
    evidence: f.evidence.map((e) => ({ label: e.label, value: e.value })),
    fixKcl: f.fixKcl,
    fixCommand: f.fixCommand,
    firstSeenAtMs: ms(f.firstSeenAt),
    lastSeenAtMs: ms(f.lastSeenAt),
    podStart: podStartOf(f.podStart),
  }));
}

export function runsOf(res: ListMlRunsResponse): MlRun[] {
  return res.runs.map((r) => ({
    id: r.id,
    name: r.name,
    project: r.project,
    template: r.template,
    orchestrator: r.orchestrator,
    clusterId: r.clusterId,
    state: runStateOf(r.state),
    params: { ...r.params },
    launchedBy: r.launchedBy,
    startedAtMs: ms(r.startedAt),
    finishedAtMs: ms(r.finishedAt),
    acceleratorCount: r.acceleratorCount,
    acceleratorProduct: r.acceleratorProduct,
    restarts: r.restarts,
    preemptions: r.preemptions,
    metrics: r.metrics.map((m) => ({ name: m.name, unit: m.unit, points: m.points.map((p) => ({ step: n(p.step), atMs: ms(p.time), value: p.value })) })),
    outputModelDigest: r.outputModelDigest,
  }));
}

export function modelsOf(res: ListMlModelsResponse): MlModel[] {
  return res.models.map((m) => ({
    name: m.name,
    project: m.project,
    repository: m.repository,
    versions: m.versions.map((v) => ({
      digest: v.digest,
      tag: v.tag,
      createdAtMs: ms(v.createdAt),
      runId: v.runId,
      sizeBytes: n(v.sizeBytes),
      evalMetrics: { ...v.evalMetrics },
      servingIn: [...v.servingIn],
    })),
  }));
}

export function inferenceOf(res: ListMlInferenceServicesResponse): MlInferenceService[] {
  return res.services.map((s) => ({
    name: s.name,
    project: s.project,
    environment: s.environment,
    clusterId: s.clusterId,
    runtime: s.runtime,
    release: s.release,
    modelDigest: s.modelDigest,
    replicas: s.replicas,
    ttftP95Ms: s.ttftP95Ms,
    queueWaitP95Ms: s.queueWaitP95Ms,
    tokensPerSecond: s.tokensPerSecond,
    acceleratorUtilizationPercent: s.acceleratorUtilizationPercent,
    kvCacheUsagePercent: s.kvCacheUsagePercent,
    coldStart: podStartOf(s.coldStart),
    releaseVerdict: verdictOf(s.releaseVerdict),
    releaseVerdictSummary: s.releaseVerdictSummary,
    releaseCheckedAtMs: ms(s.releaseCheckedAt),
  }));
}
