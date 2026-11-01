import { useId } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";
import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "../ui";
import { formatCents } from "./usageModel";

const consentSchema = z.object({
  budget: z.string().regex(/^\d+(\.\d{1,2})?$/, "Enter a positive dollar amount with at most two decimal places.")
    .refine(value => Number.isSafeInteger(Math.round(Number(value) * 100)) && Math.round(Number(value) * 100) > 0, "Enter a monthly budget above $0."),
  accepted: z.boolean().refine(Boolean, "Accept the usage billing policy to continue."),
});

export interface UsageBillingCardProps {
  enabled: boolean;
  policyVersion: string;
  agreement?: { status: string; infraBudgetCents: bigint; policyVersion?: string };
  canManage: boolean;
  /** Displayed from the authoritative billing catalog, never client prices. */
  rates: string[];
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
    resolver: zodResolver(consentSchema), defaultValues: { budget: props.agreement?.status === "pending" ? (Number(props.agreement.infraBudgetCents) / 100).toFixed(2) : "", accepted: false },
  });
  const active = props.agreement?.status === "active";
  const pending = props.agreement?.status === "pending";
  const canCheckout = !props.agreement || pending;
  return <Card><CardHeader><CardTitle>App hosting billing</CardTitle></CardHeader><CardContent className="space-y-4">
    <p className="text-sm text-muted-foreground">Pay as you go, with no base fee. One agreement covers this organization’s apps and environments. Machine subscriptions and AI wallet credits are separate; no machine purchase is required.</p>
    {props.isLoading ? <p role="status">Loading app hosting billing…</p> : props.error ? <div role="alert"><p>{props.error}</p><Button variant="outline" onClick={props.onRetry}>Check billing status</Button></div> : !props.enabled ? <div role="status"><p>{props.unavailableReason ?? "App hosting billing is unavailable. Buying a machine plan does not enable it."}</p><Button variant="outline" onClick={props.onRetry}>Check billing status</Button></div> : active ? <>
      <p role="status">App hosting billing is active.</p>
      <p>Monthly admission budget: {formatCents(Number(props.agreement!.infraBudgetCents))}. Changing the consented budget is not currently supported.</p>
      <p className="text-xs text-muted-foreground">This is not a guaranteed maximum bill. Enforcement can lag and in-flight workloads may continue accruing charges; storage is not deleted.</p>
    </> : <>
      <p role="status">{pending ? "Checkout is pending. Hosting billing is not active until the server verifies funding." : props.agreement ? `App hosting billing is not active (${props.agreement.status}). Resolve billing before starting new paid work.` : "App hosting billing is not enabled."}</p>
      {!props.canManage ? <p>Only an organization owner or admin can authorize usage billing.</p> : canCheckout ? props.rates.length === 0 || !props.policyVersion || (pending && !!props.agreement?.policyVersion && props.agreement.policyVersion !== props.policyVersion) ? <p>Usage rates or policy are unavailable. Checkout is disabled until they can be verified.</p> : <form className="space-y-4" onSubmit={handleSubmit(values => props.onCheckout({ policyVersion: props.policyVersion, infraBudgetCents: BigInt(Math.round(Number(values.budget) * 100)) }))}>
        <p className="text-sm">App CPU, memory and storage are billed from first use. Machine plans include no app hosting allowance.</p>
        <ul className="list-inside list-disc text-sm">{props.rates.map(rate => <li key={rate}>{rate}</li>)}</ul>
        <div className="forge-ui"><label htmlFor={`${fieldId}-budget`}>Monthly app hosting admission budget (USD)</label><Input id={`${fieldId}-budget`} readOnly={pending} inputMode="decimal" {...register("budget")} aria-describedby={`${fieldId}-policy`} invalid={!!errors.budget} />{errors.budget && <p role="alert">{errors.budget.message}</p>}</div>
        <p id={`${fieldId}-policy`} className="text-xs text-muted-foreground">This budget controls admission and suspension based on measured usage. It is not a guaranteed maximum bill: enforcement can lag and workloads already in flight can continue accruing charges. Storage is not deleted when the budget is reached.</p>
        <label className="flex items-start gap-2 text-sm"><input type="checkbox" {...register("accepted")} />I authorize organization-wide pay-as-you-go app hosting at the displayed rates and budget under policy {props.policyVersion}.</label>
        {errors.accepted && <p role="alert">{errors.accepted.message}</p>}
        <Button type="submit" loading={props.isSubmitting} disabled={props.isSubmitting}>{pending ? "Continue checkout" : "Authorize and continue to checkout"}</Button>
      </form> : <p>Contact your organization’s billing administrator to resolve this agreement.</p>}
      {pending && <Button variant="outline" onClick={props.onRetry}>Check activation status</Button>}
    </>}
    <p className="text-xs text-muted-foreground">App hosting authorization does not grant a new observability allowance or longer analytics retention.</p>
  </CardContent></Card>;
}
