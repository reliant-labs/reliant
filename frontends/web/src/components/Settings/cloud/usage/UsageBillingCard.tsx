import { useId } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";
import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "../ui";
import { formatCents } from "./usageModel";
import { ConsentCheckbox } from "@/components/Billing/ConsentCheckbox";

const consentSchema = z.object({
  budget: z.string().regex(/^\d+(\.\d{1,2})?$/, "Enter a positive dollar amount with at most two decimal places.")
    .refine(value => Number.isSafeInteger(Math.round(Number(value) * 100)) && Math.round(Number(value) * 100) > 0, "Enter a monthly budget above $0."),
  accepted: z.boolean().refine(Boolean, "Accept the usage billing policy to continue."),
});

/**
 * The budget a new agreement starts from: $50 a month, the same default the
 * control plane applies when a checkout names none (control-plane
 * DefaultHostingSpendCapCents). The user can change it before consenting.
 */
export const DEFAULT_HOSTING_BUDGET = "50.00";

export interface UsageBillingCardProps {
  enabled: boolean;
  policyVersion: string;
  agreement?: { status: string; infraBudgetCents: bigint; policyVersion?: string };
  canManage: boolean;
  /** Displayed from the authoritative billing catalog, never client prices. */
  rates: string[];
  /** The monthly hosting minimum, when the server states one. */
  minimumLine?: string | null;
  isLoading?: boolean;
  error?: string;
  unavailableReason?: string;
  isSubmitting?: boolean;
  onRetry: () => void;
  onCheckout: (consent: { policyVersion: string; infraBudgetCents: bigint }) => void;
}

/** Pure consent/status surface. A checkout return never makes an agreement active. */
export function UsageBillingCard(props: UsageBillingCardProps) {
  const fieldId = useId();
  const { register, handleSubmit, formState: { errors } } = useForm<z.infer<typeof consentSchema>>({
    resolver: zodResolver(consentSchema), defaultValues: { budget: props.agreement?.status === "pending" ? (Number(props.agreement.infraBudgetCents) / 100).toFixed(2) : DEFAULT_HOSTING_BUDGET, accepted: false },
  });
  const active = props.agreement?.status === "active";
  const pending = props.agreement?.status === "pending";
  const canCheckout = !props.agreement || pending;
  return <Card><CardHeader><CardTitle>App hosting billing</CardTitle></CardHeader><CardContent className="space-y-4">
    <p className="text-sm text-muted-foreground">Pay as you go for the apps you host. One agreement covers this organization’s apps and environments. Machine plans and AI credit are separate, and you don’t need a machine plan to host apps.</p>
    {props.isLoading ? <p role="status">Loading app hosting billing…</p> : props.error ? <div role="alert"><p>{props.error}</p><Button variant="outline" onClick={props.onRetry}>Check billing status</Button></div> : !props.enabled ? <div role="status"><p>{props.unavailableReason ?? "App hosting billing is unavailable. Buying a machine plan does not enable it."}</p><Button variant="outline" onClick={props.onRetry}>Check billing status</Button></div> : active ? <>
      <p role="status">App hosting billing is active.</p>
      <p>Monthly admission budget: {formatCents(Number(props.agreement!.infraBudgetCents))}. Changing the consented budget is not currently supported.</p>
      <p className="text-xs text-muted-foreground">This is not a guaranteed maximum bill. Enforcement can lag and in-flight workloads may continue accruing charges; storage is not deleted.</p>
    </> : <>
      <p role="status">{pending ? "Checkout is pending. Hosting billing is not active until the server verifies funding." : props.agreement ? `App hosting billing is not active (${props.agreement.status}). Resolve billing before starting new paid work.` : "App hosting billing is not enabled."}</p>
      {!props.canManage ? <p>Only an organization owner or admin can authorize usage billing.</p> : canCheckout ? props.rates.length === 0 || !props.policyVersion || (pending && !!props.agreement?.policyVersion && props.agreement.policyVersion !== props.policyVersion) ? <p>Usage rates or policy are unavailable. Checkout is disabled until they can be verified.</p> : <form className="space-y-4" onSubmit={handleSubmit(values => props.onCheckout({ policyVersion: props.policyVersion, infraBudgetCents: BigInt(Math.round(Number(values.budget) * 100)) }))}>
        <p className="text-sm">Hosting is billed from first use at these rates. Machine plans don’t include hosting.</p>
        <ul className="list-inside list-disc text-sm" data-testid="hosting-rates">{props.rates.map(rate => <li key={rate}>{rate}</li>)}</ul>
        {props.minimumLine && <p className="text-sm" data-testid="hosting-minimum">{props.minimumLine}</p>}
        <div className="forge-ui"><label htmlFor={`${fieldId}-budget`}>Monthly app hosting admission budget (USD)</label><Input id={`${fieldId}-budget`} readOnly={pending} inputMode="decimal" {...register("budget")} aria-describedby={`${fieldId}-policy`} invalid={!!errors.budget} />{errors.budget && <p role="alert">{errors.budget.message}</p>}</div>
        <p id={`${fieldId}-policy`} className="text-xs text-muted-foreground">When usage reaches this budget, new deployments are paused. It isn’t a hard limit on your bill: usage already running can keep adding charges for a short while. Stored data is never deleted when the budget is reached.</p>
        <ConsentCheckbox {...register("accepted")}>I agree to pay for this organization’s app hosting at the rates above{props.minimumLine ? ", including the monthly minimum," : ","} billed monthly as it is used, with the monthly budget I set.</ConsentCheckbox>
        {errors.accepted && <p role="alert">{errors.accepted.message}</p>}
        <Button type="submit" loading={props.isSubmitting} disabled={props.isSubmitting}>{pending ? "Continue checkout" : "Authorize and continue to checkout"}</Button>
      </form> : <p>Contact your organization’s billing administrator to resolve this agreement.</p>}
      {pending && <Button variant="outline" onClick={props.onRetry}>Check activation status</Button>}
    </>}
    <p className="text-xs text-muted-foreground">App hosting authorization does not grant a new observability allowance or longer analytics retention.</p>
  </CardContent></Card>;
}
