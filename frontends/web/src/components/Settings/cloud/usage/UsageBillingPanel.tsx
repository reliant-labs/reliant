import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { useActiveOrg } from "@/hooks/useOrgs";
import { useUsageBillingCheckout } from "@/hooks/useCloudBillingQueries";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { openCheckout } from "@/lib/stripeCheckout";
import { UsageBillingCard } from "./UsageBillingCard";
import { usageBillingReturnUrls } from "./usageBillingCheckout";

export function UsageBillingPanel() {
  const org = useActiveOrg();
  // Remount consent on organization change: authorization never follows a switch.
  return org.active ? <OrgUsageBillingPanel key={org.active.id} orgId={org.active.id} canManage={org.active.role === "owner" || org.active.role === "admin"} />
    : <p role="status">{org.error ? "Could not verify your organization. App hosting checkout is unavailable." : "Resolving organization for app hosting billing…"}</p>;
}

function OrgUsageBillingPanel({ orgId, canManage }: { orgId: string; canManage: boolean }) {
  const state = useQuery({
    queryKey: ["usage-billing", orgId],
    queryFn: () => getControlPlaneClient(BillingService).getCurrentUserUsageBilling({ orgId }),
    staleTime: 10_000,
    refetchOnWindowFocus: true,
    // An older server cannot serve this RPC. Avoid automatic retries, but keep
    // focus/manual refresh so a later backend rollout recovers without a reload.
    retry: (failures, error) => !(error instanceof ConnectError && error.code === Code.Unimplemented) && failures < 3,
  });
  const checkout = useUsageBillingCheckout();
  const [handoffError, setHandoffError] = useState(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const refetch = state.refetch;
  const pending = state.data?.agreement?.status === "pending";
  // Bounded polling while webhook verification is in flight; manual refresh remains.
  useEffect(() => {
    if (!pending) return;
    const interval = setInterval(() => void refetch(), 2000);
    const stop = setTimeout(() => clearInterval(interval), 60_000);
    return () => { clearInterval(interval); clearTimeout(stop); };
  }, [pending, refetch]);
  const undeployed = state.error instanceof ConnectError && state.error.code === Code.Unimplemented;
  return <div id="app-hosting"><UsageBillingCard
    unavailableReason={undeployed ? "App hosting billing is not available on this server yet. Buying a machine plan does not enable it." : undefined}
    key={`${state.data?.policyVersion ?? "loading"}:${state.data?.agreement?.id ?? "new"}`}
    enabled={!undeployed && (state.data?.enabled ?? false)}
    policyVersion={state.data?.policyVersion ?? ""}
    agreement={state.data?.agreement}
    rates={(state.data?.rates ?? []).map(rate => `${rate.displayName}: ${rate.priceCents} cents / ${rate.unit}`)}
    canManage={canManage}
    isLoading={state.isLoading}
    error={state.error && !undeployed ? "Could not verify app hosting billing. Try again before authorizing usage." : checkout.error || handoffError ? "Checkout could not be completed. Check billing status and review the policy before retrying." : undefined}
    isSubmitting={checkout.isPending}
    onRetry={() => { checkout.reset(); setHandoffError(false); void refetch(); }}
    onCheckout={async consent => {
      // Capture the org the user authorized; never resolve a different one after await.
      const urls = usageBillingReturnUrls();
      try {
        const response = await checkout.mutateAsync({ ...consent, orgId, ...urls });
        if (!mounted.current) return; // An org switch must not open the old org's checkout.
        if (!response.checkoutUrl) throw new Error("Checkout URL unavailable");
        await openCheckout(response.checkoutUrl, urls.successUrl);
        if (mounted.current) void refetch(); // An outcome is never proof of funding.
      } catch { if (mounted.current) setHandoffError(true); }
    }}
  /></div>;
}
