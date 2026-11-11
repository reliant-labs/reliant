// Copyright (c) 2025 Reliant Labs

/**
 * shop's DeliveryInsightsService: the release ledger the charts' markers,
 * Overview's Recent releases and Dashboards read, and DORA over it — the SAME
 * promotions the console's Releases and Environments show (shopStory.ts
 * demoLedger: one id, commit, promoter and time each), with the rules
 * control-plane applies (contract K5).
 */

import type { DoraMetrics, Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { TimeWindow } from "@/services/controlPlane/observability/types";

import { commitAgeMs, DAY, DEMO_ENV_IDS, demoLedger, demoRelease, HOUR, type DemoLedgerEntry } from "../shopStory";
import type { DeliveryKeys } from "./shopApi";

/** The ledger as DeliveryInsights answers it, oldest first. Scoped (targeted) deploys are not whole-release promotions there. */
export function shopPromotions(now: number): Promotion[] {
  return demoLedger(now)
    .filter((p) => !p.targets.length)
    .map((p) => ({
      promotionId: p.promotionId,
      envId: p.envId,
      envName: p.envName,
      releaseId: `rel-${p.version}`,
      version: p.version,
      gitCommit: p.commit,
      gitTag: p.version,
      promotedAtMs: p.atMs,
      promotedBy: p.by,
      promotedByUserId: p.byUserId,
      kind: p.kind,
    }));
}

type Classified = DemoLedgerEntry & { class: "deployment" | "rollback" | "reapply" };

/** contract K5: a rollback (recorded, or of a release this env ran before the running one), a re-apply, or a deployment. */
function classify(now: number): Classified[] {
  const out: Classified[] = [];
  for (const envId of DEMO_ENV_IDS) {
    let running: string | null = null;
    for (const p of demoLedger(now).filter((q) => q.envId === envId && !q.targets.length)) {
      out.push({ ...p, class: p.kind === "rollback" ? "rollback" : p.version === running ? "reapply" : "deployment" });
      running = p.version;
    }
  }
  return out.sort((a, b) => a.atMs - b.atMs);
}

const median = (xs: number[]) => {
  if (!xs.length) return null;
  const s = [...xs].sort((a, b) => a - b);
  const m = Math.floor(s.length / 2);
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};
const mean = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : null);
const goHours = (h: number) => `${h}h0m0s`;

const DEFINITIONS = {
  deploymentFrequency:
    "Deployments per day: promotions in the window that put a different release in an environment, summed across the environments in scope and divided by the window length in days. A rollback (a promotion recorded as one, or of a release older than the one running) and a re-apply of the release already running are not deployments.",
  leadTime:
    "Time from the release's source commit time to its promotion (promoted_at), over the deployments in the window whose commit time is known. Deployments whose release recorded no commit time are counted as unknown and excluded; no value is inferred for them. A commit time later than the promotion is treated as unknown.",
  changeFailureRate: (h: number) =>
    `Share of deployments in the window that were rolled back within ${goHours(h)} of being promoted (a promotion recorded as a rollback, or of a release older than the one running). A deployment younger than ${goHours(h)} that has not been rolled back counts as not failed.`,
  timeToRestore: (h: number) =>
    `Time from a failed change's promotion to the rollback that restored the previous release, over the failed changes in the window (a failed change is one rolled back within ${goHours(h)} of being promoted). Measured from the release ledger; it does not use alerts.`,
};

/** DORA over `window` for `envIds` (empty: all), from shop's ledger. */
export function shopDora(now: number, envIds: string[], window: TimeWindow, failureHours: number): DoraMetrics {
  const all = classify(now).filter((p) => !envIds.length || envIds.includes(p.envId));
  const inWindow = all.filter((p) => p.atMs >= window.startMs && p.atMs <= window.endMs);
  const deployments = inWindow.filter((p) => p.class === "deployment");
  const days = (window.endMs - window.startMs) / DAY;
  const restores: number[] = [];
  let failed = 0;
  for (const d of deployments) {
    const next = all.find((p) => p.envId === d.envId && p.atMs > d.atMs);
    if (next && next.class === "rollback" && next.atMs - d.atMs <= failureHours * HOUR) {
      failed += 1;
      restores.push(next.atMs - d.atMs);
    }
  }
  // Every release records its head commit's time: lead time is measured for all of them.
  const leads = deployments.map((d) => d.atMs - (now - commitAgeMs(demoRelease(d.version)!, 0)));
  return {
    windowStartMs: window.startMs,
    windowEndMs: window.endMs,
    failureWindowHours: failureHours,
    environmentCount: envIds.length || DEMO_ENV_IDS.length,
    deploymentFrequency: {
      definition: DEFINITIONS.deploymentFrequency,
      deployments: deployments.length,
      perDay: days > 0 ? deployments.length / days : 0,
      rollbacks: inWindow.filter((p) => p.class === "rollback").length,
      reapplies: inWindow.filter((p) => p.class === "reapply").length,
    },
    leadTime: {
      definition: DEFINITIONS.leadTime,
      measuredChanges: leads.length,
      unknownChanges: 0,
      unknownReleases: 0,
      medianMs: median(leads),
      meanMs: mean(leads),
    },
    changeFailureRate: {
      definition: DEFINITIONS.changeFailureRate(failureHours),
      changes: deployments.length,
      failedChanges: failed,
      rate: deployments.length ? failed / deployments.length : null,
    },
    timeToRestore: {
      definition: DEFINITIONS.timeToRestore(failureHours),
      source: "release_ledger",
      failures: failed,
      ongoing: 0,
      medianMs: median(restores),
      meanMs: mean(restores),
    },
    alertTimeToRestore: null,
  };
}

export function shopDelivery(now: number): Pick<ObservabilityApi, DeliveryKeys> {
  return {
    listReleases: async (req) => {
      const hits = shopPromotions(now).filter((p) => (!req.envIds.length || req.envIds.includes(p.envId)) && p.promotedAtMs >= req.window.startMs && p.promotedAtMs <= req.window.endMs);
      const limit = req.limit ?? 500;
      return { promotions: hits.slice(-limit), truncated: hits.length > limit };
    },
    getDoraMetrics: async (req) => {
      const window = req.window ?? { startMs: now - 30 * DAY, endMs: now };
      return shopDora(now, req.envIds, window, req.failureWindowHours || 24);
    },
  };
}
