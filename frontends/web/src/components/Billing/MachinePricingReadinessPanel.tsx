import { useMutation, useQuery } from "@tanstack/react-query";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { useActiveOrg } from "@/hooks/useOrgs";
import { MachinePricingReadinessCard } from "./MachinePricingReadinessCard";

/** Explicit org/plan/digest tuple, never whichever org is selected after consent. */
export function MachinePricingReadinessPanel({ planId, currentPriceLabel }: { planId: string; currentPriceLabel?: string }) {
  const org = useActiveOrg();
  return org.active ? <OrgMachineReadiness key={`${org.active.id}:${planId}`} orgId={org.active.id} planId={planId} currentPriceLabel={currentPriceLabel} canManage={org.active.role === "owner" || org.active.role === "admin"} /> : null;
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
    // A failed read renders nothing (the card needs terms to show at all); a
    // failed confirmation says so, beside the terms it was for.
    error={accept.error ? "We couldn't record your confirmation. Try again." : undefined}
    onRetry={() => { accept.reset(); void state.refetch(); }}
    onAccept={terms => { if (canManage && state.data?.activationReady && state.data.termsSha256 === terms.termsSha256) accept.mutate(terms); }}
  />;
}
