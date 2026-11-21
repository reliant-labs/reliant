import { useId, type ReactNode } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";
import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "../ui";
import { formatCents } from "./usageModel";
import { ConsentCheckbox } from "@/components/Billing/ConsentCheckbox";
import { HOSTING_BUDGET_MAX_LABEL, hostingBudgetInputValue, validateHostingBudget } from "./hostingBudget";

function checkoutConsentSchema(pendingBudget: bigint | undefined) {
  return z.object({
    budget: z.string(),
    accepted: z.boolean().refine(Boolean, "Accept the usage billing policy to continue."),
  }).transform(({ budget }, ctx) => {
    // Pending checkout resumes the exact stored consent, even if it predates
    // the maximum. Only a new consent validates the entered amount.
    if (pendingBudget !== undefined) return { infraBudgetCents: pendingBudget };
    const result = validateHostingBudget(budget);
    if (result.error !== undefined) {
      ctx.addIssue({ code: "custom", path: ["budget"], message: result.error });
      return z.NEVER;
    }
    return { infraBudgetCents: result.cents };
  });
}

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
  /** Shown in place of the read-only budget once the agreement is active (HostingBudgetEditor). */
  budgetControl?: ReactNode;
}

/** Where a person asks for hosting billing while it is not open yet (docs/help: support@reliantlabs.io). */
export const HOSTING_SUPPORT_MAILTO = `mailto:support@reliantlabs.io?subject=${encodeURIComponent("App hosting billing")}`;

/**
 * Hosting billing is switched off on this server (production's
 * USAGE_BILLING_ENABLED), or the server predates it. Said plainly, with a way
 * to ask for it — and never a pointer at machine plans, which do not enable
 * hosting and are a different product.
 */
export function HostingBillingUnavailable({ reason, onRetry }: { reason?: string; onRetry: () => void }) {
  return <div role="status" className="space-y-3" data-testid="hosting-billing-unavailable">
    <p className="text-ui text-foreground">{reason ?? "App hosting billing isn’t available yet."}</p>
    <p className="text-sm text-muted-foreground">Until it opens, this organization can still use its free static site if it holds the free allowance. To host a backend or more sites sooner, contact support and we’ll set it up with you.</p>
    <div className="flex flex-wrap gap-2">
      <a href={HOSTING_SUPPORT_MAILTO} className="inline-flex items-center text-sm font-medium text-primary-ink hover:underline" data-testid="hosting-billing-contact-support">Contact support</a>
      <Button variant="ghost" size="sm" onClick={onRetry}>Check billing status</Button>
    </div>
  </div>;
}

/** Pure consent/status surface. A checkout return never makes an agreement active. */
export function UsageBillingCard(props: UsageBillingCardProps) {
  const fieldId = useId();
  const active = props.agreement?.status === "active";
  const pending = props.agreement?.status === "pending";
  const pendingBudget = pending ? props.agreement?.infraBudgetCents : undefined;
  const consentSchema = checkoutConsentSchema(pendingBudget);
  const { register, handleSubmit, formState: { errors } } = useForm<z.input<typeof consentSchema>, unknown, z.output<typeof consentSchema>>({
    resolver: zodResolver(consentSchema), defaultValues: { budget: DEFAULT_HOSTING_BUDGET, accepted: false },
  });
  const canCheckout = !props.agreement || pending;
  return <Card><CardHeader><CardTitle>App hosting billing</CardTitle></CardHeader><CardContent className="space-y-4">
    <p className="text-ui text-muted-foreground">Pay as you go for the apps you host. One agreement covers this organization’s apps and environments. Machine plans and AI credit are separate, and you don’t need a machine plan to host apps.</p>
    {props.isLoading ? <p role="status">Loading app hosting billing…</p> : props.error ? <div role="alert"><p>{props.error}</p><Button variant="outline" onClick={props.onRetry}>Check billing status</Button></div> : !props.enabled ? <HostingBillingUnavailable reason={props.unavailableReason} onRetry={props.onRetry} /> : active ? <>
      <p role="status">App hosting billing is active.</p>
      {props.budgetControl ?? <>
        <p>Monthly budget: {formatCents(Number(props.agreement!.infraBudgetCents))}.</p>
        <p className="text-meta text-muted-foreground">This is not a guaranteed maximum bill. Enforcement can lag and in-flight workloads may continue accruing charges; storage is not deleted.</p>
      </>}
    </> : <>
      <p role="status">{pending ? "Checkout is pending. Hosting billing is not active until the server verifies funding." : props.agreement ? `App hosting billing is not active (${props.agreement.status}). Resolve billing before starting new paid work.` : "App hosting billing is not enabled."}</p>
      {!props.canManage ? <p>Only an organization owner or admin can authorize usage billing.</p> : canCheckout ? props.rates.length === 0 || !props.policyVersion || (pending && !!props.agreement?.policyVersion && props.agreement.policyVersion !== props.policyVersion) ? <p>Usage rates or policy are unavailable. Checkout is disabled until they can be verified.</p> : <form className="space-y-4" onSubmit={handleSubmit(({ infraBudgetCents }) => props.onCheckout({ policyVersion: props.policyVersion, infraBudgetCents }))}>
        <p className="text-ui">Hosting is billed from first use at these rates. Machine plans don’t include hosting.</p>
        <ul className="list-inside list-disc text-ui" data-testid="hosting-rates">{props.rates.map(rate => <li key={rate}>{rate}</li>)}</ul>
        {props.minimumLine && <p className="text-ui" data-testid="hosting-minimum">{props.minimumLine}</p>}
        <div className="forge-ui">
          <label htmlFor={`${fieldId}-budget`}>Monthly app hosting admission budget (USD)</label>
          {pendingBudget !== undefined ? (
            <Input key="pending" id={`${fieldId}-budget`} readOnly value={hostingBudgetInputValue(pendingBudget)} aria-describedby={`${fieldId}-budget-help ${fieldId}-policy`} />
          ) : (
            <Input key="new" id={`${fieldId}-budget`} inputMode="decimal" {...register("budget")} aria-describedby={`${fieldId}-budget-help ${fieldId}-policy`} invalid={!!errors.budget} />
          )}
          <p id={`${fieldId}-budget-help`} className="text-ui text-muted-foreground">
            {pending ? "Continuing checkout keeps the budget you already agreed to." : `Self-serve maximum: ${HOSTING_BUDGET_MAX_LABEL} per month.`}
          </p>
          {errors.budget && <p role="alert">{errors.budget.message}</p>}
        </div>
        <p id={`${fieldId}-policy`} className="text-meta text-muted-foreground">When usage reaches this budget, new deployments are paused. It isn’t a hard limit on your bill: usage already running can keep adding charges for a short while. Stored data is never deleted when the budget is reached.</p>
        <ConsentCheckbox {...register("accepted")}>I agree to pay for this organization’s app hosting at the rates above{props.minimumLine ? ", including the monthly minimum," : ","} billed monthly as it is used, with the monthly budget I set.</ConsentCheckbox>
        {errors.accepted && <p role="alert">{errors.accepted.message}</p>}
        <Button type="submit" loading={props.isSubmitting} disabled={props.isSubmitting}>{pending ? "Continue checkout" : "Authorize and continue to checkout"}</Button>
      </form> : <p>Contact your organization’s billing administrator to resolve this agreement.</p>}
      {pending && <Button variant="outline" onClick={props.onRetry}>Check activation status</Button>}
    </>}
    <p className="text-meta text-muted-foreground">App hosting authorization does not grant a new observability allowance or longer analytics retention.</p>
  </CardContent></Card>;
}
