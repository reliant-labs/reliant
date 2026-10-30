import { useState, type ReactNode } from "react";
import { MachineSaleContext } from "@/lib/machine-sale-terms";
import { useQuery } from "@tanstack/react-query";
import { BillingService, type GetMachinePricingReadinessResponse } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { useActiveOrg } from "@/hooks/useOrgs";
import { exactCentsLabel, parseRational } from "@/lib/exact-rational";
import Button from "@/components/forge-ui/button";

/** New paid checkout only. Existing subscriptions use separate next-period consent. */
export function MachineSaleConsent({ planId, children }: { planId: string; children: (monthlyPriceCents?: number) => ReactNode }) {
  const org = useActiveOrg();
  if (!org.active) return <p role="status">Machine checkout is unavailable until your organization is verified. Your own computer remains available.</p>;
  return <OrgMachineSale key={`${org.active.id}:${planId}`} orgId={org.active.id} planId={planId} canManage={["owner", "admin"].includes(org.active.role)}>{children}</OrgMachineSale>;
}
function OrgMachineSale({ orgId, planId, canManage, children }: { orgId: string; planId: string; canManage: boolean; children: (monthlyPriceCents?: number) => ReactNode }) {
  const readiness = useQuery({ queryKey: ["machine-pricing-readiness", orgId, planId], queryFn: () => getControlPlaneClient(BillingService).getMachinePricingReadiness({ orgId, planId, pricingVersion: "" }), retry: false, staleTime: 0 });
  if (readiness.error) return <div role="alert"><p>Machine pricing is unavailable on this server. No new paid checkout is authorized.</p><Button variant="outline" onClick={() => void readiness.refetch()}>Check pricing</Button></div>;
  if (readiness.isPending) return <p role="status">Verifying machine checkout terms…</p>;
  const terms = readiness.data;
  // A verified existing-period boundary is scoped to this org. The legacy
  // subscription read has no org_id and must not decide another org's phase.
  if (terms?.effectivePeriodStart && terms.effectivePeriodStart.seconds > 0n) return <p role="status">This organization has an existing machine period. Proposed changes require the separate next-period terms acceptance in Billing; this is not a new-subscription checkout.</p>;
  if (!terms) return <p>Machine pricing is unavailable.</p>;
  return <NewSale key={`${terms.pricingVersion}:${terms.termsSha256}`} orgId={orgId} planId={planId} canManage={canManage} terms={terms}>{children}</NewSale>;
}
function NewSale({ orgId, planId, canManage, terms, children }: { orgId: string; planId: string; canManage: boolean; terms: GetMachinePricingReadinessResponse; children: (monthlyPriceCents?: number) => ReactNode }) {
  const [accepted, setAccepted] = useState(false);
  const ready = terms.activationReady && terms.activationState === "ready_for_explicit_signup_checkout" && !terms.effectivePeriodStart && terms.holdReasons.length === 0 && !!terms.pricingVersion && !!terms.termsSha256 && !!terms.routeId && terms.monthlyPriceCents > 0n && terms.monthlyPriceCents <= BigInt(Number.MAX_SAFE_INTEGER) && terms.includedOwnSizeHours > 0n && terms.includedDiskGib > 0n && (parseRational(terms.exactHourlyRateCents)?.numerator ?? 0n) > 0n && (parseRational(terms.exactCostWeight)?.numerator ?? 0n) > 0n;
  return <div className="space-y-3">
    <h3 className="text-sm font-semibold">{ready ? "New machine subscription terms" : "Proposed machine subscription — held"}</h3>
    <p>{terms.size}: {exactCentsLabel(String(terms.monthlyPriceCents))}/month, {String(terms.includedOwnSizeHours)} own-size hours, {String(terms.includedDiskGib)} GiB disk. Extra running time {exactCentsLabel(terms.exactHourlyRateCents)}/hour. App hosting and AI are separate.</p>
    {!ready ? <div role="status"><p>New paid machine checkout is held. Proposed prices are not an approved live offer.</p><ul>{terms.holdReasons.map(reason => <li key={reason}>{reason}</li>)}</ul></div> : !canManage ? <p>Only an organization owner or admin can accept machine purchase terms.</p> : <>
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={accepted} onChange={event => setAccepted(event.target.checked)} />I accept version {terms.pricingVersion} at the displayed monthly and hourly prices for this new subscription. This is not next-period acceptance for an existing subscription.</label>
      {accepted && <MachineSaleContext.Provider value={{ orgId, planId, pricingVersion: terms.pricingVersion, termsSha256: terms.termsSha256 }}>{children(Number(terms.monthlyPriceCents))}</MachineSaleContext.Provider>}
    </>}
  </div>;
}
