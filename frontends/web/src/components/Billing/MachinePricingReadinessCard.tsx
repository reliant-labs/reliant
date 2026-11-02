import { useState } from "react";
import Button from "@/components/forge-ui/button";
import { exactCentsLabel, parseRational } from "@/lib/exact-rational";
import { formatSizeLabel } from "@/components/Settings/cloud/billingUtils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/Settings/cloud/ui";
import { ConsentCheckbox } from "./ConsentCheckbox";
import { formatPrice } from "./priceFormat";

export interface MachinePricingTerms {
  pricingVersion: string;
  activationReady: boolean;
  holdReasons: string[];
  termsSha256: string;
  monthlyPriceCents: bigint;
  includedOwnSizeHours: bigint;
  includedDiskGib: bigint;
  size: string;
  exactHourlyRateCents: string;
  exactCostWeight: string;
  routeId: string;
  activationState: string;
  effectivePeriodStart?: { seconds: bigint };
}
export interface MachinePricingReadinessCardProps {
  terms?: MachinePricingTerms;
  currentPriceLabel?: string;
  canManage: boolean;
  isLoading?: boolean;
  error?: string;
  isSubmitting?: boolean;
  acceptanceState?: string;
  acceptanceRecorded?: boolean;
  onRetry: () => void;
  onAccept: (terms: { pricingVersion: string; termsSha256: string }) => void;
}

const periodDate = (start: { seconds: bigint }) =>
  new Date(Number(start.seconds) * 1000).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });

/**
 * Next-period pricing an existing subscriber is asked to confirm.
 *
 * Shown only when there is something the customer can do or has just done:
 * terms ready to confirm, or a confirmation just recorded. A held, missing or
 * unreadable proposal is the operator's state, not the customer's, so it
 * renders nothing rather than a card explaining our internals. Staged terms
 * are never rendered as the currently charged plan.
 */
export function MachinePricingReadinessCard(props: MachinePricingReadinessCardProps) {
  const [accepted, setAccepted] = useState(false);
  const terms = props.terms;
  const complete = !!terms?.termsSha256 && !!terms.pricingVersion && !!terms.routeId && !!terms.size &&
    terms.monthlyPriceCents > 0n && terms.includedOwnSizeHours > 0n && terms.includedDiskGib > 0n &&
    (parseRational(terms.exactHourlyRateCents)?.numerator ?? 0n) > 0n && (parseRational(terms.exactCostWeight)?.numerator ?? 0n) > 0n;
  const recorded = !!props.acceptanceState || !!props.acceptanceRecorded;
  const canAccept = complete && terms?.activationReady && !!terms.effectivePeriodStart && terms.effectivePeriodStart.seconds > 0n && terms.holdReasons.length === 0 && props.canManage && !recorded;
  if (!complete || !terms?.effectivePeriodStart || (!canAccept && !recorded && !props.error)) return null;
  const size = formatSizeLabel(terms.size);
  const price = formatPrice(terms.monthlyPriceCents);
  const hours = String(terms.includedOwnSizeHours);
  const overage = exactCentsLabel(terms.exactHourlyRateCents);
  const from = periodDate(terms.effectivePeriodStart);
  return <Card><CardHeader><CardTitle>Confirm your plan's pricing</CardTitle></CardHeader><CardContent className="space-y-3">
    <p className="text-sm" data-testid="next-period-summary">From {from}: {size} machine · {price}/month · {hours} hours included · {String(terms.includedDiskGib)} GB disk · {overage}/hour after that.</p>
    {props.currentPriceLabel && <p className="text-xs text-muted-foreground">You pay {props.currentPriceLabel}/month now. Nothing changes before {from}.</p>}
    {props.error ? <div role="alert" className="space-y-2"><p className="text-sm">{props.error}</p><Button variant="outline" onClick={props.onRetry}>Try again</Button></div>
      : recorded ? <p role="status" className="text-sm">Thanks — your confirmation is recorded. It applies from {from}.</p>
      : <>
        <ConsentCheckbox checked={accepted} onChange={event => setAccepted(event.target.checked)}>
          I agree to pay {price} a month for the {size} machine plan from {from}, which includes {hours} hours of running time each month. Extra running time is {overage} an hour. The plan renews monthly, and I can cancel anytime.
        </ConsentCheckbox>
        <Button disabled={!accepted || props.isSubmitting} loading={props.isSubmitting} onClick={() => { if (accepted) props.onAccept({ pricingVersion: terms.pricingVersion, termsSha256: terms.termsSha256 }); }}>Confirm pricing</Button>
      </>}
  </CardContent></Card>;
}
