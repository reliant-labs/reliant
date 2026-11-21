import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { useActiveOrg } from "@/hooks/useOrgs";
import { useUsageBillingCheckout } from "@/hooks/useCloudBillingQueries";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { openCheckout } from "@/lib/stripeCheckout";
import { isDesktopUpdateRequired } from "@/lib/desktopUpdate";
import { UsageBillingCard } from "./UsageBillingCard";
import { usageBillingReturnUrls } from "./usageBillingCheckout";
import { hostedRateLines, hostingMinimumLine, type HostedRateLike } from "@/components/Billing/priceFormat";
import { HostingBudgetEditor } from "./HostingBudgetEditor";
import { clearHostingCheckout, hostingCheckoutStarted, HostingActivatedNotice, markHostingCheckoutStarted } from "./HostingActivatedNotice";
import { useOrgHostingUsageSummary } from "./hostingBillingQueries";

/** The hosting terms this panel discloses, as GetCurrentUserUsageBilling carries them. */
type HostedTerms = { rates: HostedRateLike[]; minimumMonthlyCents?: bigint };

const CHECKOUT_FAILED = "Checkout could not be completed. Check billing status and review the policy before retrying.";

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
    enabled: !!orgId,
    staleTime: 10_000,
    refetchOnWindowFocus: true,
  });
  const checkout = useUsageBillingCheckout();
  const [handoffError, setHandoffError] = useState<string | null>(null);
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
  const active = state.data?.agreement?.status === "active";
  // A checkout this browser started for this org, now verified by the server.
  const [activatedNotice, setActivatedNotice] = useState(() => hostingCheckoutStarted(orgId));
  // Spend so far: lowering the budget below it pauses deploys at once, so the editor warns.
  const summary = useOrgHostingUsageSummary(active && canManage ? orgId : null);
  return <div id="app-hosting" className="flex flex-col gap-4">
    {active && activatedNotice && <HostingActivatedNotice orgId={orgId} onDismiss={() => { clearHostingCheckout(orgId); setActivatedNotice(false); }} />}
    <UsageBillingCard
    budgetControl={active ? <HostingBudgetEditor
      orgId={orgId}
      budgetCents={state.data!.agreement!.infraBudgetCents}
      policyVersion={state.data?.policyVersion ?? ""}
      canManage={canManage}
      spentCents={summary.data ? Number(summary.data.spentCents) : null}
    /> : undefined}
    key={`${state.data?.policyVersion ?? "loading"}:${state.data?.agreement?.id ?? "new"}`}
    enabled={state.data?.enabled ?? false}
    policyVersion={state.data?.policyVersion ?? ""}
    agreement={state.data?.agreement}
    // Dollars in the units the price list sells in. The free egress allowance
    // and the monthly minimum come from the server (control-plane #1790:
    // HostedUsageRate.included_monthly, minimum_monthly_cents), never from
    // this client. Read structurally so this compiles before and after the
    // vendored contract carries them; until then they are absent and the
    // lines are simply not shown.
    rates={hostedRateLines((state.data?.rates ?? []) as HostedTerms["rates"])}
    minimumLine={hostingMinimumLine((state.data as HostedTerms | undefined)?.minimumMonthlyCents)}
    canManage={canManage}
    isLoading={state.isLoading}
    error={state.error ? "Could not verify app hosting billing. Try again before authorizing usage." : handoffError ?? (checkout.error ? CHECKOUT_FAILED : undefined)}
    isSubmitting={checkout.isPending}
    onRetry={() => { checkout.reset(); setHandoffError(null); void refetch(); }}
    onCheckout={async consent => {
      // Capture the org the user authorized; never resolve a different one after await.
      const urls = usageBillingReturnUrls();
      try {
        const response = await checkout.mutateAsync({ ...consent, orgId, ...urls });
        if (!mounted.current) return; // An org switch must not open the old org's checkout.
        if (!response.checkoutUrl) throw new Error("Checkout URL unavailable");
        markHostingCheckoutStarted(orgId);
        setActivatedNotice(true);
        await openCheckout(response.checkoutUrl, urls.successUrl);
        if (mounted.current) void refetch(); // An outcome is never proof of funding.
      } catch (err) {
        // An outdated desktop app cannot host checkout; say so instead of the generic failure.
        if (mounted.current) setHandoffError(isDesktopUpdateRequired(err) ? err.message : CHECKOUT_FAILED);
      }
    }}
  /></div>;
}
