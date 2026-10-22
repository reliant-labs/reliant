import { useQuery } from "@tanstack/react-query";
import { getDaemon } from "@/services/controlPlane/environments";
import { usePlans } from "@/hooks/useCloudBillingQueries";
import { hourlyPriceShort, provisionedDiskGib } from "./daemonUsage";

/** Registry telemetry is filesystem usage, not provisioned capacity. Read the
 * provisioning detail; never substitute a tier's disk for a grown machine. */
export function MachineCost({ daemonId, size, suspended = false }: { daemonId: string; size: string; suspended?: boolean }) {
  const pricing = usePlans().data?.daemonPricing;
  const detail = useQuery({
    queryKey: ["cp", "environments", "detail", daemonId],
    queryFn: async () => (await getDaemon(daemonId)) ?? null,
    refetchInterval: 15_000,
  }).data?.daemon;
  const gib = provisionedDiskGib(detail?.disk);
  const hourly = hourlyPriceShort(pricing, size);
  return <span className="text-xs text-muted-foreground" data-testid={suspended ? "machine-suspended-fee" : "machine-cost"}>
    {hourly && `${hourly} past included hours · `}
    {gib === null ? "Disk size unavailable" : `${gib} GB disk`}
  </span>;
}
