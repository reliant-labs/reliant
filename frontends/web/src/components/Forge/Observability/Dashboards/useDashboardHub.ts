// Copyright (c) 2026 Reliant Labs

import { useMemo } from "react";

import { useDashboardIndex } from "@/hooks/dashboard-queries";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { hubOf, type DashboardHub } from "./dashboardHubs";

/**
 * The hub a dashboard is a tab of, from the project's index for the scope —
 * the same cache entry the nav's stars and the Dashboards page read. Null
 * until the index answers, and for a dashboard in no hub.
 */
export function useDashboardHub(project: string, envId: string | undefined, ref: DashboardRef | null): DashboardHub | null {
  const org = useObservabilityOrg();
  const index = useDashboardIndex(org.orgId, project, envId);
  return useMemo(() => (ref && index.data ? hubOf(index.data.dashboards, ref, project) : null), [ref, index.data, project]);
}
