import { useId, useState } from "react";
import Button from "@/components/forge-ui/button";
import { exactCentsLabel, parseRational } from "@/lib/exact-rational";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/Settings/cloud/ui";

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
/** Staged terms are never rendered as the currently charged plan. */
export function MachinePricingReadinessCard(props: MachinePricingReadinessCardProps) {
  const id = useId();
  const [accepted, setAccepted] = useState(false);
  const terms = props.terms;
  const complete = !!terms?.termsSha256 && !!terms.pricingVersion && !!terms.routeId && !!terms.size &&
    terms.monthlyPriceCents > 0n && terms.includedOwnSizeHours > 0n && terms.includedDiskGib > 0n &&
    (parseRational(terms.exactHourlyRateCents)?.numerator ?? 0n) > 0n && (parseRational(terms.exactCostWeight)?.numerator ?? 0n) > 0n;
  const canAccept = complete && terms?.activationReady && !!terms.effectivePeriodStart && terms.effectivePeriodStart.seconds > 0n && terms.holdReasons.length === 0 && props.canManage && !props.acceptanceState && !props.acceptanceRecorded;
  return <Card><CardHeader><CardTitle>Proposed machine pricing</CardTitle></CardHeader><CardContent className="space-y-3">
    {props.currentPriceLabel && <p>Current subscription: {props.currentPriceLabel}. The proposal below does not change current-period charges.</p>}
    {props.isLoading ? <p role="status">Checking machine pricing readiness…</p> : props.error ? <div role="alert"><p>{props.error}</p><Button variant="outline" onClick={props.onRetry}>Check readiness</Button></div> : !terms || !complete ? <p>Proposed pricing is unavailable. No new purchase or price change can be authorized from this view.</p> : <>
      <p className="font-medium">Proposed {terms.size}: {exactCentsLabel(String(terms.monthlyPriceCents))}/month, {String(terms.includedOwnSizeHours)} own-size running hours and {String(terms.includedDiskGib)} GiB persistent disk.</p>
      <p>Extra running time: {exactCentsLabel(terms.exactHourlyRateCents)}/hour. Other sizes use the versioned equivalent-hours exchange, not a dollar credit. App hosting and AI are separate.</p>
      <p className="text-xs text-muted-foreground">Version {terms.pricingVersion} · route {terms.routeId}</p>
      {!terms.activationReady || terms.holdReasons.length > 0 ? <div role="status"><p>Proposed pricing is held; it is not available for activation.</p><ul>{terms.holdReasons.map(reason => <li key={reason}>{reason}</li>)}</ul></div> : null}
      {terms.effectivePeriodStart ? <p>Proposed next-period boundary: {new Date(Number(terms.effectivePeriodStart.seconds) * 1000).toLocaleString()}.</p> : <p>No existing next billing period is reported. This is not a new-subscription checkout.</p>}
      {props.acceptanceState || props.acceptanceRecorded ? <p role="status">Next-period acceptance recorded ({props.acceptanceState || "pending"}). Pending verification — not activated, not a charge authorization for this period.</p> : <>
        {!props.canManage && <p>Only this organization’s owner or admin can accept proposed terms.</p>}
        {canAccept && <><label htmlFor={id} className="flex gap-2 text-sm"><input id={id} type="checkbox" checked={accepted} onChange={event => setAccepted(event.target.checked)} />I accept these exact proposed terms for the next billing period only. Consent does not activate prices or schedule Stripe by itself.</label>
          <Button disabled={!accepted || props.isSubmitting} loading={props.isSubmitting} onClick={() => { if (accepted) props.onAccept({ pricingVersion: terms.pricingVersion, termsSha256: terms.termsSha256 }); }}>Accept next-period terms</Button></>}
      </>}
    </>}
    <p className="text-xs text-muted-foreground">These are proposed terms, not an approved live offer. Commercial approval, commissioning, published prices and period reconciliation must all be verified before activation. A billing hold must not destroy machine data or interrupt conversation recovery.</p>
  </CardContent></Card>;
}
