// Copyright (c) 2026 Reliant Labs

import { useRouteOrgIdOverride } from "@/lib/orgScope";
import { useActiveOrgStore } from "@/store/activeOrgStore";

/**
 * The org Usage & billing reports on: the committed route's (`/o/<org>/usage`)
 * even before the tab's effective org has caught up, nothing while the
 * route's membership is pending (never the previous tab's), and the tab's org
 * on a legacy, org-less URL.
 */
export function useUsageOrgId(): string {
  const routeOrgId = useRouteOrgIdOverride();
  const tabOrgId = useActiveOrgStore((s) => s.effectiveId);
  return (routeOrgId === undefined ? tabOrgId : routeOrgId) ?? "";
}
