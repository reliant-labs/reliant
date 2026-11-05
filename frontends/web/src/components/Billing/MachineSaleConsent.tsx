import { useState, type ReactNode } from "react";
import { MachineSaleContext } from "@/lib/machine-sale-terms";
import { useQuery } from "@tanstack/react-query";
import { BillingService, type GetMachinePricingReadinessResponse } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { useActiveOrg } from "@/hooks/useOrgs";
import { exactCentsLabel, parseRational } from "@/lib/exact-rational";
import Button from "@/components/forge-ui/button";
import { formatSizeLabel } from "@/components/Settings/cloud/billingUtils";
import { ConsentCheckbox } from "./ConsentCheckbox";
import { MachinePlanChange } from "./MachinePlanChange";
import { friendlyCheckoutHolds } from "./holdReasons";
import { formatPrice } from "./priceFormat";

interface MachineSaleConsentProps {
  planId: string;
  children: (monthlyPriceCents?: number) => ReactNode;
  /**
   * The free way forward when machine checkout cannot proceed (pricing
   * unreachable, held, or the organization unverified): onboarding passes
   * "use your own computer instead". Without it the screen offers only a retry.
   */
  onUseOwnComputer?: () => void;
  /** An existing subscriber's plan change finished (its Done button). */
  onPlanChanged?: () => void;
}

/**
 * New paid checkout. An organization that already has a machine subscription
 * is never sold a second one: it gets the plan change (MachinePlanChange,
 * an upgrade now or a switch at the next period) instead.
 */
export function MachineSaleConsent({ planId, children, onUseOwnComputer, onPlanChanged }: MachineSaleConsentProps) {
  const org = useActiveOrg();
  if (!org.active) return <CheckoutUnavailable message="Cloud machines can be bought once your organization is verified." onUseOwnComputer={onUseOwnComputer} />;
  return <OrgMachineSale key={`${org.active.id}:${planId}`} orgId={org.active.id} planId={planId} canManage={["owner", "admin"].includes(org.active.role)} onUseOwnComputer={onUseOwnComputer} onPlanChanged={onPlanChanged}>{children}</OrgMachineSale>;
}
function OrgMachineSale({ orgId, planId, canManage, children, onUseOwnComputer, onPlanChanged }: { orgId: string; planId: string; canManage: boolean; children: (monthlyPriceCents?: number) => ReactNode; onUseOwnComputer?: () => void; onPlanChanged?: () => void }) {
  const readiness = useQuery({ queryKey: ["machine-pricing-readiness", orgId, planId], queryFn: () => getControlPlaneClient(BillingService).getMachinePricingReadiness({ orgId, planId, pricingVersion: "" }), retry: false, staleTime: 0 });
  const retry = () => void readiness.refetch();
  if (readiness.error) return <CheckoutUnavailable role="alert" message="We couldn't load machine pricing just now." onRetry={retry} onUseOwnComputer={onUseOwnComputer} />;
  if (readiness.isPending) return <p role="status" className="text-sm text-muted-foreground">Loading machine pricing…</p>;
  const terms = readiness.data;
  // A verified existing-period boundary is scoped to this org. The legacy
  // subscription read has no org_id and must not decide another org's phase.
  // The readiness answer carries `effectivePeriodStart` for an organization
  // that already has a machine subscription: it changes plan (ChangeMachinePlan),
  // it never mounts the new-subscription payment.
  if (terms?.effectivePeriodStart && terms.effectivePeriodStart.seconds > 0n) return <MachinePlanChange planId={planId} onDone={onPlanChanged} />;
  if (!terms) return <CheckoutUnavailable message="Machine pricing is unavailable right now." onRetry={retry} onUseOwnComputer={onUseOwnComputer} />;
  return <NewSale key={`${terms.pricingVersion}:${terms.termsSha256}`} orgId={orgId} planId={planId} canManage={canManage} terms={terms} onRetry={retry} onUseOwnComputer={onUseOwnComputer}>{children}</NewSale>;
}

function CheckoutUnavailable({ message, role = "status", onRetry, onUseOwnComputer }: { message: string; role?: "status" | "alert"; onRetry?: () => void; onUseOwnComputer?: () => void }) {
  return <div role={role} className="space-y-3">
    {message && <p className="text-sm text-foreground">{message}</p>}
    {(onUseOwnComputer || onRetry) && <div className="flex flex-wrap gap-2">
      {onUseOwnComputer && <Button onClick={onUseOwnComputer}>Use your own computer instead (free)</Button>}
      {onRetry && <Button variant="outline" onClick={onRetry}>Try again</Button>}
    </div>}
  </div>;
}

/** Every figure the summary and the consent state; any missing one means no sale. */
function completeTerms(terms: GetMachinePricingReadinessResponse): boolean {
  return !!terms.pricingVersion && !!terms.termsSha256 && !!terms.routeId && !!terms.size && terms.monthlyPriceCents > 0n && terms.monthlyPriceCents <= BigInt(Number.MAX_SAFE_INTEGER) && terms.includedOwnSizeHours > 0n && terms.includedDiskGib > 0n && (parseRational(terms.exactHourlyRateCents)?.numerator ?? 0n) > 0n && (parseRational(terms.exactCostWeight)?.numerator ?? 0n) > 0n;
}

function NewSale({ orgId, planId, canManage, terms, children, onRetry, onUseOwnComputer }: { orgId: string; planId: string; canManage: boolean; terms: GetMachinePricingReadinessResponse; children: (monthlyPriceCents?: number) => ReactNode; onRetry: () => void; onUseOwnComputer?: () => void }) {
  const [accepted, setAccepted] = useState(false);
  const complete = completeTerms(terms);
  const holds = terms.holdReasons ?? [];
  const ready = complete && terms.activationReady && terms.activationState === "ready_for_explicit_signup_checkout" && !terms.effectivePeriodStart && holds.length === 0;
  // Never a sentence built from absent figures (": $0.00/month, 0 hours").
  if (!complete && holds.length === 0) return <CheckoutUnavailable message="Machine pricing is unavailable right now." onRetry={onRetry} onUseOwnComputer={onUseOwnComputer} />;
  if (!ready) return <div className="space-y-3">
    <h3 className="text-sm font-semibold">Machine checkout is paused</h3>
    <ul className="list-inside list-disc text-sm text-muted-foreground">{friendlyCheckoutHolds(holds).map(message => <li key={message}>{message}</li>)}</ul>
    <CheckoutUnavailable message={onUseOwnComputer ? "You can still finish setup and run Reliant on your own computer." : ""} onRetry={onRetry} onUseOwnComputer={onUseOwnComputer} />
  </div>;
  const size = formatSizeLabel(terms.size);
  const price = formatPrice(terms.monthlyPriceCents);
  const hours = String(terms.includedOwnSizeHours);
  const overage = exactCentsLabel(terms.exactHourlyRateCents);
  return <div className="space-y-3">
    <h3 className="text-sm font-semibold">Your machine plan</h3>
    <p className="text-sm" data-testid="machine-sale-summary">{size} machine · {price}/month · {hours} hours included · {String(terms.includedDiskGib)} GB disk · {overage}/hour after that</p>
    <p className="text-xs text-muted-foreground">App hosting and AI credit are billed separately.</p>
    {!canManage ? <p className="text-sm">Only an organization owner or admin can buy a machine plan.</p> : <>
      <ConsentCheckbox checked={accepted} onChange={event => setAccepted(event.target.checked)}>
        I agree to pay {price} a month for the {size} machine plan, which includes {hours} hours of running time each month. Extra running time is {overage} an hour. The plan renews monthly, and I can cancel anytime.
      </ConsentCheckbox>
      {accepted && <MachineSaleContext.Provider value={{ orgId, planId, pricingVersion: terms.pricingVersion, termsSha256: terms.termsSha256 }}>{children(Number(terms.monthlyPriceCents))}</MachineSaleContext.Provider>}
    </>}
  </div>;
}
