// Copyright (c) 2026 Reliant Labs

/**
 * One site's Real Experience Score, for a page that shows a site's speed
 * beside other things (the static site's page, Services/SiteDetail): the Speed
 * view's summary panel (speedInsightsModel.ts), narrowed to the site's
 * telemetry services, read and scored here so every caller scores alike.
 *
 *   const speed = useSiteSpeedSummary({ project, envId, services: siteServiceNames(project, site) });
 *   speed.score  // 0–100, or null with no measured visit
 *
 * The defaults are the Speed view's own (desktop, P75, the last 7 days), so
 * the figure matches what "View site insights" opens.
 */

import { useMemo } from "react";

import { useObservabilityOrg, usePanelQuery } from "@/hooks/observability-queries";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import {
  DEFAULT_DEVICE,
  DEFAULT_PERCENTILE,
  rateScore,
  realExperienceScore,
  speedPanel,
  vitalsRows,
  type Device,
  type Percentile,
  type Rating,
  type VitalsRow,
} from "./speedInsightsModel";

/** The Speed view's default window. */
export const SITE_SPEED_DEFAULT_RANGE: RangePreset = "7d";

export interface SiteSpeedSummaryOptions {
  /** The forge project. */
  project: string;
  /** One environment's control-plane id; omitted, every environment. */
  envId?: string;
  /** The site's telemetry service names (siteServiceNames); empty, every site of the project. */
  services: readonly string[];
  range?: RangePreset;
  device?: Device;
  pct?: Percentile;
  enabled?: boolean;
}

export interface SiteSpeedSummary {
  /** The Real Experience Score, 0–100; null when no scored vital was measured. */
  score: number | null;
  rating: Rating | null;
  /** Page loads measured in the window on the device. */
  views: number;
  /** The summary row itself (each vital and its split), when there is one. */
  summary: VitalsRow | undefined;
  isLoading: boolean;
  error: unknown;
  /** The underlying panel read: refetch, isFetching. */
  query: ReturnType<typeof usePanelQuery>;
}

/** The observability scope of one project and (optionally) one environment; null until the org is known. */
export function useSiteInsightsScope(project: string, envId: string | undefined): ObsScope | null {
  const org = useObservabilityOrg();
  return useMemo<ObsScope | null>(() => (org.orgId ? { orgId: org.orgId, projectId: project, envIds: envId ? [envId] : [], filters: [] } : null), [org.orgId, project, envId]);
}

export function useSiteSpeedSummary({
  project,
  envId,
  services,
  range = SITE_SPEED_DEFAULT_RANGE,
  device = DEFAULT_DEVICE,
  pct = DEFAULT_PERCENTILE,
  enabled = true,
}: SiteSpeedSummaryOptions): SiteSpeedSummary {
  const scope = useSiteInsightsScope(project, envId);
  const panel = useMemo(() => speedPanel("summary", device, pct, range, project), [device, pct, range, project]);
  // A stable array for the query key: the caller's may be rebuilt every render.
  const serviceKey = services.join("\u0000");
  const serviceList = useMemo(() => (serviceKey ? serviceKey.split("\u0000") : []), [serviceKey]);
  const query = usePanelQuery(scope, range, panel, serviceList, [], { enabled });
  const summary = vitalsRows(query.data)[0];
  const score = summary ? realExperienceScore(summary.vitals) : null;
  return { score, rating: rateScore(score), views: summary?.views ?? 0, summary, isLoading: query.isLoading, error: query.error, query };
}
