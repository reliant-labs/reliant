// Copyright (c) 2025 Reliant Labs

/**
 * Release markers: a moment on every time-series chart where a release was
 * promoted into an environment in scope.
 *
 * A dashboard asks for them by declaring `annotations: [{source: "releases"}]`
 * (contract K4); the data comes from the release ledger through
 * DeliveryInsightsService.ListReleases (contract K5). Until that RPC is wired,
 * the ObservabilityApi has no `listReleaseMarkers` and this hook answers
 * `unavailable` — the dashboard says so instead of drawing nothing silently.
 * Wiring K5 is one entry on the API seam; nothing here or in the charts
 * changes.
 */

import { useQuery } from "@tanstack/react-query";

import { obsKeys } from "@/hooks/observability-queries";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { obsRetry } from "@/services/controlPlane/observability/errors";
import type { ObsScope, ReleaseMarker } from "@/services/controlPlane/observability/types";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";

import type { ChartMarker } from "./charts/UPlotChart";

export type ReleaseMarkersStatus = "off" | "unavailable" | "loading" | "error" | "ready";

export interface ReleaseMarkers {
  status: ReleaseMarkersStatus;
  markers: ChartMarker[];
  /** How many promotions the window holds (a chart may show fewer: only those in its range). */
  count: number;
}

/** A promotion as a chart marker: the version, and the environment when more than one is in view. */
export function toChartMarkers(releases: ReleaseMarker[], withEnv: boolean): ChartMarker[] {
  return releases.map((r) => {
    const version = r.version || r.releaseId.slice(0, 12);
    return {
      timeSec: Math.round(r.promotedAtMs / 1000),
      label: withEnv ? `${r.envName} ${version}` : version,
      description: `${version} promoted to ${r.envName}${r.promotedBy ? ` by ${r.promotedBy}` : ""}`,
      tone: r.kind === "rollback" ? "warning" : "primary",
    };
  });
}

/**
 * The release markers for `scope` over `range`, when `wanted` (the dashboard
 * declares a releases annotation). Shares one cache entry per scope and window.
 */
export function useReleaseMarkers(scope: ObsScope | null, range: RangePreset, wanted: boolean): ReleaseMarkers {
  const api = useObservabilityApi();
  const list = api.listReleaseMarkers;
  const query = useQuery({
    queryKey: [...obsKeys.all(scope), "release-markers", range] as const,
    queryFn: () => list!({ ...(scope as ObsScope), window: resolveWindow(range) }),
    enabled: wanted && !!scope && !!list,
    staleTime: 60_000,
    retry: obsRetry,
  });
  if (!wanted) return { status: "off", markers: [], count: 0 };
  if (!list) return { status: "unavailable", markers: [], count: 0 };
  if (query.isLoading) return { status: "loading", markers: [], count: 0 };
  if (query.error || !query.data) return { status: "error", markers: [], count: 0 };
  const envs = new Set(query.data.map((r) => r.envId));
  return { status: "ready", markers: toChartMarkers(query.data, envs.size > 1), count: query.data.length };
}
