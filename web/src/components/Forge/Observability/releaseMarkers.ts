// Copyright (c) 2025 Reliant Labs

/**
 * Release markers: a moment on every time-series chart where a release was
 * promoted into an environment in scope.
 *
 * The panel asks the release ledger (DeliveryInsightsService.ListReleases,
 * contract K5) once per scope and window, and an ObsChartMarkersProvider puts
 * the answer on every chart below it — Overview's service charts, Workloads,
 * and every dashboard that declares `annotations: [{source: "releases"}]`
 * (contract K4). The scope is the charts' own: org, project, the environments
 * in view, and any environment named by a filter (deliveryScope.ts).
 */

import { useMemo } from "react";

import { useReleases } from "@/hooks/observability-queries";
import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";

import type { ChartMarker } from "./charts/UPlotChart";
import { deliveryScopeOf, shortCommit } from "./deliveryScope";
import type { ObsEnvironment } from "./obsPlace";

export type ReleaseMarkersStatus = "off" | "loading" | "error" | "ready";

export interface ReleaseMarkers {
  status: ReleaseMarkersStatus;
  markers: ChartMarker[];
  /** Newest first, for the list beside the charts. */
  promotions: Promotion[];
  /** The ledger held more than the list's limit: these are the newest. */
  truncated: boolean;
  /** The filters exclude every environment, so no promotion can be in view. */
  excluded: boolean;
  error: unknown;
  refetch: () => void;
}

const NONE: Omit<ReleaseMarkers, "status" | "refetch"> = { markers: [], promotions: [], truncated: false, excluded: false, error: null };

/** One line naming a promotion: what, where, from which commit, and by whom. */
export function describePromotion(p: Promotion): string {
  const version = p.version || p.releaseId.slice(0, 12);
  const commit = p.gitCommit ? ` (commit ${shortCommit(p.gitCommit)})` : "";
  const who = p.promotedBy ? ` by ${p.promotedBy}` : "";
  return p.kind === "rollback" ? `${p.envName} rolled back to ${version}${commit}${who}` : `${version}${commit} promoted to ${p.envName}${who}`;
}

/** Promotions as chart markers: the version, with the environment when more than one is in view; a rollback in the warning tone. */
export function toChartMarkers(promotions: readonly Promotion[], withEnv: boolean): ChartMarker[] {
  return promotions.map((p) => {
    const version = p.version || p.releaseId.slice(0, 12);
    const short = p.kind === "rollback" ? `↩ ${version}` : version;
    return {
      timeSec: Math.round(p.promotedAtMs / 1000),
      label: withEnv ? `${p.envName} ${short}` : short,
      description: describePromotion(p),
      tone: p.kind === "rollback" ? "warning" : "primary",
    };
  });
}

/** The release markers for the telemetry `scope` over `range`, when `enabled`. */
export function useReleaseMarkers(
  scope: ObsScope | null,
  range: RangePreset,
  environments: readonly ObsEnvironment[],
  enabled: boolean
): ReleaseMarkers {
  const delivery = useMemo(() => (scope ? deliveryScopeOf(scope, environments) : null), [scope, environments]);
  const query = useReleases(delivery, `range:${range}`, () => resolveWindow(range), { enabled: enabled && !!delivery });
  const refetch = () => void query.refetch();
  return useMemo<ReleaseMarkers>(() => {
    if (!enabled || !scope) return { ...NONE, status: "off", refetch };
    if (!delivery) return { ...NONE, status: "ready", excluded: true, refetch };
    if (query.isLoading) return { ...NONE, status: "loading", refetch };
    if (query.error || !query.data) return { ...NONE, status: "error", error: query.error, refetch };
    const promotions = query.data.promotions;
    const envs = new Set(promotions.map((p) => p.envId));
    return {
      ...NONE,
      status: "ready",
      markers: toChartMarkers(promotions, envs.size > 1),
      promotions: [...promotions].reverse(),
      truncated: query.data.truncated,
      refetch,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, scope, delivery, query.isLoading, query.error, query.data]);
}
