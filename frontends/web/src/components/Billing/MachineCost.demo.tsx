// Copyright (c) 2026 Reliant Labs

/**
 * components/Billing/MachineCost.tsx in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo's Agent sandboxes page
 * (demoAgentsRoutes.tsx) has no plan to price a machine against, so a row
 * says only its disk. The real cost reads the account's price list
 * (useCloudBillingQueries usePlans), billing code the demo bundle must not
 * contain.
 */

import { useGetDaemon } from "@/hooks/controlplane/daemon-service-hooks_gen";

import type * as Real from "./MachineCost";
import { provisionedDiskGib } from "./daemonUsage";

export const MachineCost: typeof Real.MachineCost = ({ daemonId, suspended = false }) => {
  const gib = provisionedDiskGib(useGetDaemon({ daemonId }).data?.daemon?.disk);
  return (
    <span className="text-xs text-muted-foreground" data-testid={suspended ? "machine-suspended-fee" : "machine-cost"}>
      {gib === null ? "Disk size unavailable" : `${gib} GB disk`}
    </span>
  );
};
