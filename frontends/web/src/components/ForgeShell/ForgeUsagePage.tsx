// Copyright (c) 2025 Reliant Labs

/**
 * /usage (and /o/<org>/usage), on both hosts: the organization's "Spend and usage" view,
 * narrowed to what forge provisions — hosting compute, static hosting and
 * egress, managed databases, machines, builds and registry, observability —
 * with managed LLM left out (OrganizationUsageView scope="forge",
 * forgeUsage.ts). Caps, wallet and plans are in Settings › Billing.
 */

import { OrganizationUsageView } from "@/components/Forge/Observability/Dashboards/OrganizationUsageView";
import { usagePath } from "@/lib/paths";
import { useActiveOrgStore } from "@/store/activeOrgStore";

import { AreaShell } from "../Layout/AreaShell";

/** The route's page (routes.tsx): the view in the app's area chrome, like Apps and the Inbox. */
export function UsagePage() {
  return (
    <AreaShell areaPath={usagePath()} areaLabel="Usage" areaNoun="usage">
      <ForgeUsageView />
    </AreaShell>
  );
}

/** The view alone. */
export function ForgeUsageView() {
  const orgId = useActiveOrgStore((s) => s.effectiveId) ?? "";
  return (
    <div data-testid="forge-usage-page">
      <OrganizationUsageView orgId={orgId} scope="forge" />
    </div>
  );
}
