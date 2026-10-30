// Copyright (c) 2025 Reliant Labs

/**
 * control-plane `controlplane.v1.DeliveryInsightsService`, mapped into the
 * console's model (./deliveryTypes.ts). The only file that knows its wire shape.
 *
 * The server checks membership of `org_id` first (PermissionDenied for a
 * non-member), then that every env id is a live environment of that org — and
 * of the project when one is set — with one PermissionDenied for "missing",
 * "deleted" and "another org's" alike.
 */

import { create } from "@bufbuild/protobuf";
import { DurationSchema, TimestampSchema, type Duration, type Timestamp } from "@bufbuild/protobuf/wkt";

import { DeliveryInsightsService, type ReleaseMarker as WirePromotion } from "@/gen/controlplane/services/delivery_insights/v1/delivery_insights_pb";
import {
  ObservabilityService,
  ReleaseFindingCode as WireFindingCode,
  ReleaseVerdict as WireVerdict,
  type GetReleaseHealthResponse,
  type ServiceWindowStats as WireWindowStats,
  type TimeRange as WireTimeRange,
} from "@/gen/controlplane/services/observability/v1/observability_pb";

import { getControlPlaneClient } from "../client";
import { obsClient } from "./obsClient";
import { msOf } from "./api";
import {
  RELEASES_LIMIT,
  type DeliveryScope,
  type DoraMetrics,
  type Promotion,
  type PromotionList,
  type ReleaseFindingCode,
  type ReleaseHealth,
  type ReleaseHealthRequest,
  type ReleaseVerdict,
  type ServiceWindowStats,
  type TimeToRestore,
} from "./deliveryTypes";
import { countOf, mapIssue } from "./issuesApi";
import type { TimeWindow } from "./types";

function client() {
  return getControlPlaneClient(DeliveryInsightsService);
}

function timestamp(ms: number): Timestamp {
  const seconds = Math.floor(ms / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

/** A Duration as milliseconds; an unset one (no value to report) as null. */
export function durationMs(d: Duration | undefined): number | null {
  if (!d) return null;
  return Number(d.seconds) * 1000 + d.nanos / 1e6;
}

/** The DeliveryScope on the wire. Only the shared fields: services and filters are reserved there. */
export function wireDeliveryScope(s: DeliveryScope, window: TimeWindow | undefined) {
  return {
    orgId: s.orgId,
    projectId: s.projectId || undefined,
    envIds: s.envIds,
    timeRange: window ? { start: timestamp(window.startMs), end: timestamp(window.endMs) } : undefined,
  };
}

function mapPromotion(p: WirePromotion): Promotion {
  return {
    promotionId: p.promotionId,
    envId: p.environmentId,
    envName: p.environmentName,
    releaseId: p.releaseId,
    version: p.version,
    gitCommit: p.gitCommit,
    gitTag: p.gitTag,
    promotedAtMs: msOf(p.promotedAt),
    promotedBy: p.promotedBy,
    promotedByUserId: p.promotedByUserId,
    kind: p.kind,
  };
}

/** Promotions in `window` (required by the server), oldest first; the newest `limit` when there are more. */
export async function listReleases(req: DeliveryScope & { window: TimeWindow; limit?: number }): Promise<PromotionList> {
  const res = await client().listReleases({
    scope: wireDeliveryScope(req, req.window),
    limit: Math.min(req.limit ?? RELEASES_LIMIT.default, RELEASES_LIMIT.max),
  });
  return { promotions: res.releases.map(mapPromotion), truncated: res.truncated };
}

/**
 * DORA over `window` (the server's default, the last 30 days, when absent),
 * with a deployment counted as failed when rolled back within
 * `failureWindowHours` (0: the server's 24).
 */
export async function getDoraMetrics(req: DeliveryScope & { window?: TimeWindow; failureWindowHours?: number }): Promise<DoraMetrics> {
  const res = await client().getDoraMetrics({
    scope: wireDeliveryScope(req, req.window),
    failureWindowHours: req.failureWindowHours ?? 0,
  });
  const df = res.deploymentFrequency;
  const lt = res.leadTime;
  const cfr = res.changeFailureRate;
  const ttr = res.timeToRestore;
  const restore = (t: typeof ttr): TimeToRestore => ({
    definition: t?.definition ?? "",
    source: t?.source ?? "",
    failures: t?.failures ?? 0,
    medianMs: durationMs(t?.median),
    meanMs: durationMs(t?.mean),
    ongoing: t?.ongoing ?? 0,
  });
  return {
    windowStartMs: msOf(res.windowStart),
    windowEndMs: msOf(res.windowEnd),
    failureWindowHours: res.failureWindowHours,
    environmentCount: res.environmentCount,
    deploymentFrequency: {
      definition: df?.definition ?? "",
      deployments: df?.deployments ?? 0,
      perDay: df?.perDay ?? 0,
      rollbacks: df?.rollbacks ?? 0,
      reapplies: df?.reapplies ?? 0,
    },
    leadTime: {
      definition: lt?.definition ?? "",
      measuredChanges: lt?.measuredChanges ?? 0,
      unknownChanges: lt?.unknownChanges ?? 0,
      unknownReleases: lt?.unknownReleases ?? 0,
      medianMs: durationMs(lt?.median),
      meanMs: durationMs(lt?.mean),
    },
    changeFailureRate: {
      definition: cfr?.definition ?? "",
      changes: cfr?.changes ?? 0,
      failedChanges: cfr?.failedChanges ?? 0,
      rate: cfr?.rate ?? null,
    },
    timeToRestore: restore(ttr),
    // Unset is "no figure", not zero incidents: only a present message is mapped.
    alertTimeToRestore: res.alertTimeToRestore ? restore(res.alertTimeToRestore) : null,
  };
}

// ── Release health ───────────────────────────────────────────────────────────

const VERDICT_OF: Record<WireVerdict, ReleaseVerdict> = {
  [WireVerdict.UNSPECIFIED]: "unknown",
  [WireVerdict.PASS]: "pass",
  [WireVerdict.FAIL]: "fail",
  [WireVerdict.INCONCLUSIVE]: "inconclusive",
};

const FINDING_CODE_OF: Record<WireFindingCode, ReleaseFindingCode | null> = {
  [WireFindingCode.UNSPECIFIED]: null,
  [WireFindingCode.ERROR_RATE]: "error_rate",
  [WireFindingCode.LATENCY]: "latency",
  [WireFindingCode.NEW_ISSUE]: "new_issue",
  [WireFindingCode.REGRESSED_ISSUE]: "regressed_issue",
  [WireFindingCode.ALERT_FIRING]: "alert_firing",
  [WireFindingCode.WORKLOAD_RESTARTS]: "workload_restarts",
};

function windowOf(r: WireTimeRange | undefined): TimeWindow | null {
  return r?.start && r.end ? { startMs: msOf(r.start), endMs: msOf(r.end) } : null;
}

function statsOf(s: WireWindowStats | undefined): ServiceWindowStats | null {
  if (!s) return null;
  return { requests: countOf(s.requests), errorRate: s.errorRate, p50Ms: s.p50Ms, p95Ms: s.p95Ms, p99Ms: s.p99Ms };
}

function duration(ms: number): Duration {
  const seconds = Math.floor(ms / 1000);
  return create(DurationSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

export function mapReleaseHealth(res: GetReleaseHealthResponse): ReleaseHealth {
  const p = res.promotion;
  return {
    promotion: p
      ? { promotionId: p.id, version: p.releaseVersion, gitCommit: p.gitCommit, promotedAtMs: msOf(p.promotedAt), previousVersion: p.previousReleaseVersion }
      : null,
    before: windowOf(res.before),
    after: windowOf(res.after),
    afterComplete: res.afterComplete,
    services: res.services.map((s) => ({ service: s.service, before: statsOf(s.before), after: statsOf(s.after), versionShare: s.versionShare })),
    newIssues: res.newIssues.map(mapIssue),
    regressedIssues: res.regressedIssues.map(mapIssue),
    firingAlerts: res.firingAlerts.map((a) => ({ rule: a.rule, fingerprint: a.fingerprint, firingAtMs: msOf(a.firingAt), labels: { ...a.labels } })),
    workloadFindings: res.workloadFindings.map((w) => ({ workload: w.workload, restarts: countOf(w.restarts), lastTerminatedReason: w.lastTerminatedReason })),
    verdict: VERDICT_OF[res.verdict] ?? "unknown",
    findings: res.findings.map((f) => ({
      code: FINDING_CODE_OF[f.code] ?? null,
      service: f.service,
      value: f.value,
      threshold: f.threshold,
      policy: f.policy,
      message: f.message,
    })),
    thresholds: res.thresholds.map((t) => ({ name: t.name, value: t.value, source: t.source })),
    notEvaluated: [...res.notEvaluated],
  };
}

/**
 * The server's verdict on one promotion into one environment (the current one
 * when `promotionId` is absent). The scope must name exactly one environment;
 * the windows come from the promotion, so no time range is sent.
 */
export async function getReleaseHealth(req: ReleaseHealthRequest, options: { signal?: AbortSignal } = {}): Promise<ReleaseHealth> {
  const now = Date.now();
  const res = await obsClient(ObservabilityService).getReleaseHealth({
    // time_range is required on every QueryScope and ignored here (the
    // promotion decides the windows): the last minute satisfies the validator.
    scope: { ...wireDeliveryScope(req, { startMs: now - 60_000, endMs: now }), services: [], filters: [] },
    subject: req.promotionId ? { case: "promotionId", value: req.promotionId } : { case: undefined },
    window: req.windowMs ? duration(req.windowMs) : undefined,
  }, options);
  return mapReleaseHealth(res);
}
