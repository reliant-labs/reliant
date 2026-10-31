import { useMutation, useQuery } from "@tanstack/react-query";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { useActiveOrg } from "@/hooks/useOrgs";
import { MachinePricingReadinessCard } from "./MachinePricingReadinessCard";

/** Explicit org/plan/digest tuple, never whichever org is selected after consent. */
export function MachinePricingReadinessPanel({ planId, currentPriceLabel }: { planId: string; currentPriceLabel?: string }) {
  const org = useActiveOrg();
  return org.active ? <OrgMachineReadiness key={`${org.active.id}:${planId}`} orgId={org.active.id} planId={planId} currentPriceLabel={currentPriceLabel} canManage={org.active.role === "owner" || org.active.role === "admin"} /> : <p>Machine pricing readiness is unavailable until the organization can be verified.</p>;
}
function OrgMachineReadiness({ orgId, planId, canManage, currentPriceLabel }: { orgId: string; planId: string; canManage: boolean; currentPriceLabel?: string }) {
  const state = useQuery({
    queryKey: ["machine-pricing-readiness", orgId, planId],
    queryFn: () => getControlPlaneClient(BillingService).getMachinePricingReadiness({ orgId, planId, pricingVersion: "" }),
    retry: false, staleTime: 30_000,
  });
  const accept = useMutation({
    mutationFn: (terms: { pricingVersion: string; termsSha256: string }) => getControlPlaneClient(BillingService).acceptNextMachinePricing({ orgId, planId, ...terms }),
  });
  return <MachinePricingReadinessCard
    key={state.data?.termsSha256 ?? "unknown"}
    terms={state.data}
    currentPriceLabel={currentPriceLabel}
    canManage={canManage}
    isLoading={state.isLoading}
    isSubmitting={accept.isPending}
    acceptanceState={accept.data?.activationState}
    acceptanceRecorded={accept.isSuccess}
    error={state.error ? "Machine pricing readiness is unavailable on this server. No price change has been authorized." : accept.error ? "Acceptance could not be verified. Check readiness before trying again; no activation can be inferred." : undefined}
    onRetry={() => { accept.reset(); void state.refetch(); }}
    onAccept={terms => { if (canManage && state.data?.activationReady && state.data.termsSha256 === terms.termsSha256) accept.mutate(terms); }}
  />;
}
