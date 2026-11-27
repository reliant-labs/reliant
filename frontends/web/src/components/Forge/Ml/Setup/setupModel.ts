// Copyright (c) 2026 Reliant Labs

/**
 * The Setup page's view-model: header numbers across clusters, and each
 * coverage row's reading (covered, a gap, or not applicable). Pure; tested
 * in __tests__/setupModel.test.ts.
 */

import { MlComponentState, type MlClusterSetup, type MlCoverageItem } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { tsMs } from "../kit";

export const isGap = (i: Pick<MlCoverageItem, "state">) => i.state === MlComponentState.MISSING || i.state === MlComponentState.PARTIAL;

export interface SetupKpis {
  clusters: number;
  coverage: number;
  missing: number;
  partial: number;
  lastDiscoveryMs: number | null;
  oldestDiscoveryMs: number | null;
}

export function setupKpis(clusters: readonly MlClusterSetup[]): SetupKpis {
  const items = clusters.flatMap((c) => c.coverage);
  const times = clusters.map((c) => tsMs(c.discoveredAt)).filter((t): t is number => t !== null);
  return {
    clusters: clusters.length,
    coverage: clusters.length ? clusters.reduce((a, c) => a + c.coverageRatio, 0) / clusters.length : 0,
    missing: items.filter((i) => i.state === MlComponentState.MISSING).length,
    partial: items.filter((i) => i.state === MlComponentState.PARTIAL).length,
    lastDiscoveryMs: times.length ? Math.max(...times) : null,
    oldestDiscoveryMs: times.length ? Math.min(...times) : null,
  };
}

/** Gaps first (missing, then partial), then covered, then not applicable; declared order within. */
export function orderCoverage(items: readonly MlCoverageItem[]): MlCoverageItem[] {
  const rank = (s: MlComponentState) => (s === MlComponentState.MISSING ? 0 : s === MlComponentState.PARTIAL ? 1 : s === MlComponentState.NOT_APPLICABLE ? 3 : 2);
  return items
    .map((item, i) => ({ item, i }))
    .sort((a, b) => rank(a.item.state) - rank(b.item.state) || a.i - b.i)
    .map((x) => x.item);
}

/** The ?sel of an open KCL disclosure: "<cluster_id>:<item key>". */
export const kclSel = (clusterId: string, key: string) => `${clusterId}:${key}`;
