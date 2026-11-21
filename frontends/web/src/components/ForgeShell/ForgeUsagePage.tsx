// Copyright (c) 2025 Reliant Labs

/**
 * /usage in the forge shell: the organization's "Spend and usage" view,
 * narrowed to what forge provisions — hosting compute, static hosting and
 * egress, managed databases, machines, builds and registry, observability —
 * with managed LLM left out (OrganizationUsageView scope="forge",
 * forgeUsage.ts). Caps, wallet and plans are in Settings › Billing.
 */

import { OrganizationUsageView } from "@/components/Forge/Observability/Dashboards/OrganizationUsageView";
import { useRouteOrgIdOverride } from "@/lib/orgScope";
import { useActiveOrgStore } from "@/store/activeOrgStore";

export function ForgeUsagePage() {
  const routeOrgId = useRouteOrgIdOverride();
  const tabOrgId = useActiveOrgStore((s) => s.effectiveId);
  const orgId = (routeOrgId === undefined ? tabOrgId : routeOrgId) ?? "";
  return (
    <div className="-mx-4 -mt-4 sm:-mx-6" data-testid="forge-usage-page">
      <OrganizationUsageView orgId={orgId} scope="forge" />
    </div>
  );
}
