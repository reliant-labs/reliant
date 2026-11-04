// Copyright (c) 2026 Reliant Labs
import { useQuery } from "@tanstack/react-query";
import Card, { CardHeader } from "@/components/forge-ui/card";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { useEnvironmentShapes } from "@/hooks/forge-queries";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { BillingUnavailable } from "./ObservabilityLimits";

/**
 * Existing Deploy release-declared shape, never a new billing policy endpoint.
 * The environment list is the roster; each declared environment's shape (its
 * observability policy) is fetched per environment.
 */
export function ObservabilityCollectionPolicy({ orgId }: { orgId: string }) {
  const policy = useQuery({ queryKey: ["observability-billing", orgId, "collection-policy"],
    queryFn: () => getControlPlaneClient(DeployService).listEnvironments({}), retry: false, staleTime: 60_000 });
  const environments = policy.data?.environments ?? [];
  const shapes = useEnvironmentShapes(environments.map(env => (env.declaredAt ? env.id : "")));
  return <Card><CardHeader title="What we collect" description="Read-only release configuration reported by Deploy for your environments." />
    {policy.isPending ? <p role="status" className="text-sm">Loading collection policy…</p> : policy.error ? <BillingUnavailable label="Collection policy" error={policy.error} retry={() => void policy.refetch()} /> : <>
      {!environments.length && <p className="text-sm text-muted-foreground">No deployed environments reported yet.</p>}
      <ul className="divide-y divide-border text-sm">{environments.map(env => {
        const entry = shapes.get(env.id);
        const shape = entry?.shape;
        const obs = shape?.observability;
        return <li key={env.id} className="space-y-1 py-3"><p className="font-medium">{env.project ? `${env.project} / ` : ""}{env.name}</p>
          <p>{entry?.isLoading ? "Loading…" : entry?.error ? "Couldn't load this environment's release configuration." : !shape ? "No release configuration reported yet." : obs ? !obs.enabled ? "Observability disabled in this release." : `Observability enabled. Disabled signals: ${obs.disabled.join(", ") || "None declared"}.` : "This release does not report a collection policy."}</p>
          <p className="text-xs text-muted-foreground">Replay and trace sampling details are not reported by this release API.</p>
        </li>;
      })}</ul>
    </>}
    <p className="mt-3 text-xs text-muted-foreground">Change collection in your project's forge.k (Observability.signals), then deploy. These settings are not editable in billing.</p>
  </Card>;
}
