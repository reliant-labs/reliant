// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's DeliveryInsightsService: the release ledger the delivery
 * views chart and DORA over it — shop's story (model/delivery.ts) encoded on
 * the wire, so the console's real client
 * (services/controlPlane/observability/deliveryApi.ts) decodes it. Release
 * health is ObservabilityService's (handlers/observability.ts). A relative
 * window resolves against `clock` (the pack clock by default).
 */

import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { DeliveryInsightsService } from "@/gen/controlplane/services/delivery_insights/v1/delivery_insights_pb";
import type { TimeToRestore } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createShopApi } from "../model/shopApi";
import { completed } from "./defaults";
import { demoNow } from "../shopStory";
import { durOpt, ts, windowOf, type PackClock } from "./wire";

const wireRestore = (t: TimeToRestore) => ({
  definition: t.definition,
  source: t.source,
  failures: t.failures,
  median: durOpt(t.medianMs),
  mean: durOpt(t.meanMs),
  ongoing: t.ongoing,
});

export function deliveryHandlers(fixtures: ObservabilityApi = createShopApi(), { clock = demoNow }: { clock?: PackClock } = {}): MockHandlers {
  return completed(DeliveryInsightsService, mockService(DeliveryInsightsService, {
    listReleases: async (req) => {
      const scope = req.scope;
      const res = await fixtures.listReleases({
        orgId: scope?.orgId ?? "",
        projectId: scope?.projectId,
        envIds: [...(scope?.envIds ?? [])],
        window: windowOf(scope?.timeRange, clock) ?? { startMs: 0, endMs: 0 },
        limit: req.limit,
      });
      return {
        releases: res.promotions.map((p) => ({
          promotionId: p.promotionId,
          environmentId: p.envId,
          environmentName: p.envName,
          releaseId: p.releaseId,
          version: p.version,
          gitCommit: p.gitCommit,
          gitTag: p.gitTag,
          promotedAt: ts(p.promotedAtMs),
          promotedBy: p.promotedBy,
          promotedByUserId: p.promotedByUserId,
          kind: p.kind,
        })),
        truncated: res.truncated,
      };
    },
    getDoraMetrics: async (req) => {
      const scope = req.scope;
      const d = await fixtures.getDoraMetrics({
        orgId: scope?.orgId ?? "",
        projectId: scope?.projectId,
        envIds: [...(scope?.envIds ?? [])],
        window: windowOf(scope?.timeRange, clock),
        failureWindowHours: req.failureWindowHours,
      });
      return {
        windowStart: ts(d.windowStartMs),
        windowEnd: ts(d.windowEndMs),
        failureWindowHours: d.failureWindowHours,
        environmentCount: d.environmentCount,
        deploymentFrequency: { ...d.deploymentFrequency },
        leadTime: {
          definition: d.leadTime.definition,
          measuredChanges: d.leadTime.measuredChanges,
          unknownChanges: d.leadTime.unknownChanges,
          unknownReleases: d.leadTime.unknownReleases,
          median: durOpt(d.leadTime.medianMs),
          mean: durOpt(d.leadTime.meanMs),
        },
        changeFailureRate: {
          definition: d.changeFailureRate.definition,
          changes: d.changeFailureRate.changes,
          failedChanges: d.changeFailureRate.failedChanges,
          rate: d.changeFailureRate.rate ?? undefined,
        },
        timeToRestore: wireRestore(d.timeToRestore),
        alertTimeToRestore: d.alertTimeToRestore ? wireRestore(d.alertTimeToRestore) : undefined,
      };
    },
  }));
}
