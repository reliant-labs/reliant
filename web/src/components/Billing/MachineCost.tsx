import { useQuery } from "@tanstack/react-query";
import { getDaemon } from "@/services/controlPlane/environments";
import { usePlans } from "@/hooks/useCloudBillingQueries";
import { hourlyPriceShort, provisionedDiskGib, suspendedFeeLabel } from "./daemonUsage";

/** Registry telemetry is filesystem usage, not provisioned capacity. Read the
 * provisioning detail; never substitute a tier's disk for a grown machine. */
export function MachineCost({ daemonId, size, suspended = false }: { daemonId: string; size: string; suspended?: boolean }) {
  const pricing = usePlans().data?.daemonPricing;
  const detail = useQuery({
    queryKey: ["cp", "environments", "detail", daemonId],
    queryFn: async () => (await getDaemon(daemonId)) ?? null,
    refetchInterval: 15_000,
  }).data?.daemon;
  const gib = provisionedDiskGib(pricing, size, detail?.disk);
  const fee = gib === null ? null : suspendedFeeLabel(pricing, gib);
  const hourly = hourlyPriceShort(pricing, size);
  const standard = pricing?.sizes.find((s) => s.size === size);
  const excess = gib !== null && standard ? Math.max(0, gib - Number(standard.storageGib)) : 0;
  return <span className="text-xs text-muted-foreground" data-testid={suspended ? "machine-suspended-fee" : "machine-cost"}>
    {hourly && `${hourly} past included hours · `}
    {gib === null ? "Disk cost unavailable" : `${gib} GiB disk · ${fee ?? "Fee unavailable"} while suspended until deleted`}
    {excess > 0 && ` · ${suspendedFeeLabel(pricing, excess)} extra disk while running`}
  </span>;
}
