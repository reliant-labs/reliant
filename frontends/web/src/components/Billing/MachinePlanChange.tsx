import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";
import {
  BillingService,
  type ChangeMachinePlanResponse,
  type PreviewMachinePlanChangeResponse,
} from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { useActiveOrg } from "@/hooks/useOrgs";
import { cloudBillingKeys } from "@/hooks/useCloudBillingQueries";
import { errorReason } from "@/lib/errorReasons";
import Button from "@/components/forge-ui/button";

/**
 * Changing an existing machine subscription's plan (control-plane
 * ChangeMachinePlan; design: guaranteed-machine-repricing.md §11).
 *
 * An upgrade applies now and charges the prorated difference today. A
 * downgrade waits for the next billing period and charges nothing. Choosing
 * the current plan while a downgrade is scheduled withdraws it. Every figure
 * here is the server's quote; the confirmation sends that quote's digest back,
 * so the customer is charged exactly what this panel showed.
 *
 * `onDone` and `onCancel` are optional: a surface whose own controls move
 * on (the checkout's plan tiles) passes neither, and their buttons are left out.
 */
export function MachinePlanChange({
  planId,
  onDone,
  onCancel,
}: {
  planId: string;
  onDone?: () => void;
  onCancel?: () => void;
}) {
  const org = useActiveOrg();
  // The plan being quoted. It starts as the one the customer picked; a
  // scheduled downgrade's "keep my plan" swaps it for the current plan.
  const [target, setTarget] = useState(planId);
  if (!org.active) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        Plan changes are available once your organization has loaded.
      </p>
    );
  }
  // The quote is an owner/admin request (it lists every member's machines),
  // so a member is told who can change the plan rather than shown an error.
  if (org.active.role !== "owner" && org.active.role !== "admin") {
    return (
      <div className="space-y-3 rounded-xl border border-border bg-card p-5">
        <p className="text-sm text-foreground">Only an organization owner or admin can change the plan.</p>
        {onCancel && (
          <Button variant="ghost" onClick={onCancel}>
            Back to plans
          </Button>
        )}
      </div>
    );
  }
  return (
    <OrgMachinePlanChange
      key={`${org.active.id}:${target}`}
      orgId={org.active.id}
      planId={target}
      onKeepCurrentPlan={setTarget}
      onDone={onDone}
      onCancel={onCancel}
    />
  );
}

const sizeLabel = (size: string) => size.toUpperCase();
const dollars = (cents: bigint | number) => `$${(Number(cents) / 100).toFixed(2)}`;
const dayLabel = (seconds?: bigint) =>
  seconds === undefined
    ? "your next billing date"
    : new Date(Number(seconds) * 1000).toLocaleDateString(undefined, { month: "long", day: "numeric" });

// The server's refusals are written for the customer (svcerr keeps the
// message of a code it chose), so show them; anything else gets a safe line.
function messageOf(err: unknown): string {
  const raw = err instanceof ConnectError ? err.rawMessage : (err as { rawMessage?: unknown } | null)?.rawMessage;
  if (typeof raw === "string" && raw) return raw;
  return "The plan change could not be completed. Nothing was charged.";
}

function OrgMachinePlanChange({
  orgId,
  planId,
  onDone,
  onCancel,
  onKeepCurrentPlan,
}: {
  orgId: string;
  planId: string;
  onDone?: () => void;
  onCancel?: () => void;
  onKeepCurrentPlan: (currentPlanId: string) => void;
}) {
  const queryClient = useQueryClient();
  const quote = useQuery({
    queryKey: ["machine-plan-change", orgId, planId],
    queryFn: () => getControlPlaneClient(BillingService).previewMachinePlanChange({ orgId, planId }),
    retry: false,
    staleTime: 0,
    // A quote binds for ten minutes; refresh it well inside that.
    refetchInterval: 5 * 60_000,
  });
  const change = useMutation({
    mutationFn: (q: PreviewMachinePlanChangeResponse) =>
      getControlPlaneClient(BillingService).changeMachinePlan({
        orgId,
        planId,
        pricingVersion: q.pricingVersion,
        prorationDate: q.prorationDate,
        termsSha256: q.termsSha256,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: cloudBillingKeys.computeSubscription });
      void queryClient.invalidateQueries({ queryKey: ["cloud-billing"] });
      void queryClient.invalidateQueries({ queryKey: ["machine-plan-change", orgId] });
    },
    onError: (err) => {
      const reason = errorReason(err);
      if (reason === "quote_changed" || reason === "quote_expired") void quote.refetch();
    },
  });

  if (quote.isPending) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        Working out the price of this change…
      </p>
    );
  }
  if (quote.error || !quote.data) {
    return (
      <div role="alert" className="space-y-3 rounded-xl border border-border bg-card p-5">
        <p className="text-sm text-foreground">{messageOf(quote.error)}</p>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => void quote.refetch()}>
            Try again
          </Button>
          {onCancel && (
            <Button variant="ghost" onClick={onCancel}>
              Back to plans
            </Button>
          )}
        </div>
      </div>
    );
  }
  if (change.data) {
    return <ChangeResult result={change.data} quote={quote.data} onDone={onDone} />;
  }
  const q = quote.data;
  const reason = change.error ? errorReason(change.error) : null;
  return (
    <div className="space-y-4 rounded-xl border border-border bg-card p-5" data-testid="machine-plan-change">
      <QuoteSummary q={q} />
      {!q.allowed && <Blocked q={q} />}
      {q.scheduledPlanId && q.direction !== "cancel_downgrade" && (
        <Button variant="outline" onClick={() => onKeepCurrentPlan(q.fromPlanId)}>
          Cancel the scheduled switch
        </Button>
      )}
      {change.error && (
        <div role="alert" className="space-y-1 text-sm">
          <p className="text-foreground">{messageOf(change.error)}</p>
          {(reason === "quote_changed" || reason === "quote_expired") && (
            <p className="text-muted-foreground">The price above has been refreshed.</p>
          )}
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <Button
          disabled={!q.allowed || change.isPending || quote.isFetching}
          onClick={() => change.mutate(q)}
        >
          {change.isPending ? "Confirming…" : confirmLabel(q)}
        </Button>
        {onCancel && (
          <Button variant="ghost" onClick={onCancel} disabled={change.isPending}>
            Cancel
          </Button>
        )}
      </div>
    </div>
  );
}

function confirmLabel(q: PreviewMachinePlanChangeResponse): string {
  switch (q.direction) {
    case "upgrade":
      return q.amountDueTodayCents > 0n
        ? `Upgrade and pay ${dollars(q.amountDueTodayCents)}`
        : `Upgrade to ${sizeLabel(q.newSize)}`;
    case "downgrade":
      return `Switch to ${sizeLabel(q.newSize)} on ${dayLabel(q.effectiveAt?.seconds)}`;
    default:
      return "Keep my current plan";
  }
}

function QuoteSummary({ q }: { q: PreviewMachinePlanChangeResponse }) {
  const size = sizeLabel(q.newSize);
  const renewal = dayLabel(q.periodEnd?.seconds);
  if (q.direction === "upgrade") {
    return (
      <div className="space-y-2">
        <h3 className="text-sm font-semibold text-foreground">Upgrade to {size} now</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Charged today</dt>
          <dd className="font-medium text-foreground" data-testid="plan-change-due-today">
            {dollars(q.amountDueTodayCents)}
          </dd>
          <dt className="text-muted-foreground">Then</dt>
          <dd className="text-foreground">
            {dollars(q.newMonthlyPriceCents)}/month from {renewal} (now {dollars(q.currentMonthlyPriceCents)})
          </dd>
          <dt className="text-muted-foreground">Hours</dt>
          <dd className="text-foreground">
            +{q.includedHoursDelta} {size} hours for the rest of this period; {String(q.newIncludedHours)} hours a
            month after that
          </dd>
        </dl>
        <p className="text-xs text-muted-foreground">
          Today's charge covers the price difference for the rest of this billing period. Hours you've already used
          stay counted, and the larger machine sizes are available as soon as the payment goes through.
        </p>
      </div>
    );
  }
  if (q.direction === "downgrade") {
    return (
      <div className="space-y-2">
        <h3 className="text-sm font-semibold text-foreground">Switch to {size} at your next billing period</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Charged today</dt>
          <dd className="font-medium text-foreground" data-testid="plan-change-due-today">
            $0.00
          </dd>
          <dt className="text-muted-foreground">From {renewal}</dt>
          <dd className="text-foreground">
            {dollars(q.newMonthlyPriceCents)}/month (now {dollars(q.currentMonthlyPriceCents)}),{" "}
            {String(q.newIncludedHours)} hours a month
          </dd>
        </dl>
        <p className="text-xs text-muted-foreground">
          You keep your current plan, its hours and its machine sizes until {renewal}. You can cancel the switch
          before then.
        </p>
      </div>
    );
  }
  return (
    <div className="space-y-2">
      <h3 className="text-sm font-semibold text-foreground">Keep your current plan</h3>
      <p className="text-sm text-foreground">
        Your plan is set to change on {renewal}. Keeping it cancels that change; nothing is charged.
      </p>
    </div>
  );
}

function Blocked({ q }: { q: PreviewMachinePlanChangeResponse }) {
  return (
    <div role="status" className="space-y-2 text-sm">
      {q.blockedReasons.map((r) => (
        <p key={r} className="text-foreground">
          {r.charAt(0).toUpperCase() + r.slice(1)}.
        </p>
      ))}
      {q.blockingMachines.length > 0 && (
        <ul className="list-disc pl-5 text-muted-foreground" data-testid="plan-change-blocking-machines">
          {q.blockingMachines.map((m) => (
            <li key={m.daemonId}>
              {m.name} ({sizeLabel(m.size)})
            </li>
          ))}
        </ul>
      )}
      {q.blockingMachines.length > 0 && (
        <a className="text-sm font-medium text-primary underline-offset-4 hover:underline" href="/settings/machines">
          Resize machines
        </a>
      )}
    </div>
  );
}

function ChangeResult({
  result,
  quote,
  onDone,
}: {
  result: ChangeMachinePlanResponse;
  quote: PreviewMachinePlanChangeResponse;
  onDone?: () => void;
}) {
  const size = sizeLabel(quote.newSize);
  const text =
    result.state === "confirming"
      ? "We're confirming the change with our payment provider. It completes on its own within a few minutes; you won't be charged twice."
      : result.direction === "upgrade"
        ? `You're on ${size} now. ${dollars(result.amountDueTodayCents)} was charged.`
        : result.direction === "downgrade"
          ? `Your plan switches to ${size} on ${dayLabel(result.effectiveAt?.seconds)}.`
          : "Your plan stays as it is.";
  return (
    <div role="status" className="space-y-3 rounded-xl border border-border bg-card p-5">
      <p className="text-sm text-foreground">{text}</p>
      {onDone && <Button onClick={onDone}>Done</Button>}
    </div>
  );
}
