// Copyright (c) 2026 Reliant Labs
import { useQuery } from "@tanstack/react-query";
import Card, { CardHeader } from "@/components/forge-ui/card";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { toLiveShape } from "@/services/forge/live";
import { BillingUnavailable } from "./ObservabilityLimits";

/** Existing Deploy release-declared shape, never a new billing policy endpoint. */
export function ObservabilityCollectionPolicy({ orgId }: { orgId: string }) {
  const policy = useQuery({ queryKey: ["observability-billing", orgId, "collection-policy"],
    queryFn: () => getControlPlaneClient(DeployService).listEnvironments({}), retry: false, staleTime: 60_000 });
  return <Card><CardHeader title="What we collect" description="Read-only release configuration reported by Deploy for your environments." />
    {policy.isPending ? <p role="status" className="text-sm">Loading collection policy…</p> : policy.error ? <BillingUnavailable label="Collection policy" error={policy.error} retry={() => void policy.refetch()} /> : <>
      {!policy.data.environments.length && <p className="text-sm text-muted-foreground">No deployed environments reported yet.</p>}
      <ul className="divide-y divide-border text-sm">{policy.data.environments.map(env => {
        const shape = toLiveShape(env.declaredShape);
        const obs = shape?.observability;
        return <li key={env.id} className="space-y-1 py-3"><p className="font-medium">{env.project ? `${env.project} / ` : ""}{env.name}</p>
          <p>{!shape ? "No release configuration reported yet." : obs ? !obs.enabled ? "Observability disabled in this release." : `Observability enabled. Disabled signals: ${obs.disabled.join(", ") || "None declared"}.` : "This release does not report a collection policy."}</p>
          <p className="text-xs text-muted-foreground">Replay and trace sampling details are not reported by this release API.</p>
        </li>;
      })}</ul>
    </>}
    <p className="mt-3 text-xs text-muted-foreground">Change collection in your project's forge.k (Observability.signals), then deploy. These settings are not editable in billing.</p>
  </Card>;
}
