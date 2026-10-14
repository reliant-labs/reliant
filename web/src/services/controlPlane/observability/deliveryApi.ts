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
import { TimestampSchema, type Duration, type Timestamp } from "@bufbuild/protobuf/wkt";

import { DeliveryInsightsService, type ReleaseMarker as WirePromotion } from "@/gen/controlplane/services/delivery_insights/v1/delivery_insights_pb";

import { getControlPlaneClient } from "../client";
import { msOf } from "./api";
import { RELEASES_LIMIT, type DeliveryScope, type DoraMetrics, type Promotion, type PromotionList, type TimeToRestore } from "./deliveryTypes";
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
    // Field 9 (alert-based MTTR, A8) is reserved on the wire and not sent yet.
    alertTimeToRestore: null,
  };
}
