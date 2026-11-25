import { useGetDaemon } from "@/hooks/controlplane/daemon-service-hooks_gen";
import { usePlans } from "@/hooks/useCloudBillingQueries";
import { hourlyPriceShort, provisionedDiskGib } from "./daemonUsage";

/** Registry telemetry is filesystem usage, not provisioned capacity. Read the
 * provisioning detail; never substitute a tier's disk for a grown machine. */
export function MachineCost({ daemonId, size, suspended = false }: { daemonId: string; size: string; suspended?: boolean }) {
  const pricing = usePlans().data?.daemonPricing;
  const detail = useGetDaemon({ daemonId }, { refetchInterval: 15_000 }).data?.daemon;
  const gib = provisionedDiskGib(detail?.disk);
  const hourly = hourlyPriceShort(pricing, size);
  return <span className="text-xs text-muted-foreground" data-testid={suspended ? "machine-suspended-fee" : "machine-cost"}>
    {hourly && `${hourly} past included hours · `}
    {gib === null ? "Disk size unavailable" : `${gib} GB disk`}
  </span>;
}
