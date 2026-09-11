/**
 * The compute plan tiles, and the fine print that explains what the hours mean.
 *
 * ── Why this is its own module now ────────────────────────────────────
 *
 * These used to live inside `ComputeSubscriptionCheckout`, which meant the
 * price of a machine was only visible on the page that asked for a card. The
 * onboarding flow now separates DECIDING from PAYING — the compute step shows
 * what each machine costs and lets the user choose, and the checkout step
 * later collects one card for everything owed — so both surfaces need these
 * tiles and neither owns them.
 *
 * Extracting rather than copying is the point: a second tile implementation
 * would be a second declaration of what a plan costs, and the two would drift.
 * The catalog is a server fact, and there is exactly one renderer for it.
 */

import { CheckCircle2, Loader2 } from "lucide-react";

import {
  formatCentsAsDollars,
  type DaemonSizeName,
} from "@/components/Settings/cloud/billingUtils";
import { cn } from "@/lib/utils";

/**
 * One purchasable plan, reduced to what these tiles render.
 *
 * The tile shows `label` rather than deriving one, because the two callers
 * choose along different axes: onboarding presents ONE plan per machine size
 * ("Small", "Medium" — size and plan are a single question there), while
 * settings presents the catalog by plan name. Deriving the label here would
 * force one of them to lie about what the user is picking.
 */
export interface ComputePlanOption {
  planId: string;
  label: string;
  /** The machine size this tile represents, when the caller selects by size. */
  size?: DaemonSizeName;
  monthlyPriceCents: number;
  /** -1 means unlimited; the tile says so rather than printing a number. */
  includedMinutes: number;
  /** Per-minute overage rate, in cents. 0 when the plan defines none. */
  overageCentsPerMinute: number;
}

export function PlanTiles({
  plans,
  loading,
  selectedPlanId,
  onSelect,
  coveredPlanId,
}: {
  plans: ComputePlanOption[];
  loading?: boolean;
  selectedPlanId: string | undefined;
  onSelect: (option: ComputePlanOption) => void;
  /**
   * The ONE plan this user's existing entitlement already pays for, shown as
   * "Covered" instead of a monthly price.
   *
   * A single id rather than a boolean, because coverage is per-size and not
   * blanket: a redeemed compute grant buys machine TIME at the free plan's
   * allowance (small), so marking every tile covered would promise an XL the
   * server refuses at provisioning. The remaining tiles keep their prices,
   * which is what makes picking a bigger one legible as a purchase.
   */
  coveredPlanId?: string;
}) {
  if (loading) {
    return (
      <div className="flex items-center gap-2 px-1 py-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" /> Loading plans…
      </div>
    );
  }
  if (plans.length === 0) return null;

  return (
    <div className="space-y-2">
      {plans.map((plan) => (
        <PlanTileRow
          key={plan.planId}
          plan={plan}
          selected={plan.planId === selectedPlanId}
          covered={plan.planId === coveredPlanId}
          onSelect={() => onSelect(plan)}
        />
      ))}
    </div>
  );
}

/**
 * ONE row of the machine list.
 *
 * Exported as a primitive because onboarding's list is not only plans: the
 * user's own computer is a row in it, and it is not a plan — it has no
 * `planId`, writes no `computePlanId`, and opens connect instructions instead
 * of recording a purchase. Its heading ("Choose your machine") covers every
 * machine, so the free row must be a SIBLING of the priced rows, not a block
 * beside the list.
 *
 * The alternative was a `leadingRow` slot on PlanTiles, and it is worse in the
 * way that matters here: PlanTiles owns the loading and empty states, so a
 * slot would make the free option disappear while the catalog loads or fails
 * — exactly when the one machine that needs no catalog is most useful. Giving
 * the caller the row and letting it own its own container keeps that
 * independent, and keeps ONE declaration of what a row looks like. The state
 * lives on the row rather than in it (`selected`, `covered` as props) because
 * a row cannot know which of the two lists it is in.
 */
export function PlanTileRow({
  plan,
  selected,
  covered,
  onSelect,
}: {
  plan: ComputePlanOption;
  selected: boolean;
  covered?: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={cn(
        "flex w-full items-center justify-between gap-4 rounded-lg border px-4 py-3 text-left transition-colors",
        selected
          ? "border-primary bg-primary/10"
          : "border-border bg-background hover:border-primary/40 hover:bg-muted/50",
      )}
    >
      {/* The row names the MACHINE and its price. Nothing else.
      
          It used to carry "N hours included each month" under the size, and
          that line was wrong in the way a per-row number always is when the
          quantity does not vary per row: every compute plan in the catalog
          grants the same 9600 minutes (see daemon_compute_included_minutes in
          control-plane's plans.yaml — 9600 at small, medium, large and xl
          alike). Printing it four times implied four allowances and invited
          exactly the reading it got, that a bigger machine buys more hours. It
          does not; it buys a bigger machine.
      
          The figures on screen were worse than merely redundant. They came
          from a dev database still seeded with migration 00054's superseded
          per-plan values (1000/2500/5000/-1), so the rows read 17/42/83 hours
          and Unlimited — three wrong numbers and one wrong claim, all rendered
          faithfully from stale data. A uniform fact stated per row is a
          standing invitation for drift like that to show up as product copy.
      
          Where the hours belong is once, beside the other things true of every
          hosted machine — which is what the caller does. */}
      <span className="min-w-0">
        <span className="flex items-center gap-2 text-sm font-semibold text-foreground">
          {plan.label}
          {selected && <CheckCircle2 className="h-4 w-4 text-primary" />}
        </span>
      </span>
      <span className="flex-shrink-0 text-sm font-semibold text-foreground">
        {covered ? (
          <span className="rounded-full bg-primary/10 px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider text-primary">
            Covered
          </span>
        ) : (
          <>{formatCentsAsDollars(plan.monthlyPriceCents)}/mo</>
        )}
      </span>
    </button>
  );
}

/**
 * The included hours, as one sentence to sit ONCE beneath a list of machines.
 *
 * Replaces the per-row `describeIncluded`. The allowance is the same at every
 * size, so a per-row figure said the same thing four times while implying it
 * varied — and, being fed straight from the catalog, printed four DIFFERENT
 * stale numbers the moment a dev database lagged the config.
 *
 * Still read from the plan rather than hardcoded: 9600 is control-plane's
 * value to change, and this must follow it. It takes the plan whose row is
 * selected, and callers render it only where the whole list shares that
 * allowance — the uniformity is the caller's fact to know, not this
 * function's.
 */
export function describeIncludedHours(plan: ComputePlanOption): string {
  if (plan.includedMinutes < 0) return "Unlimited machine hours are included.";
  return `${Math.round(plan.includedMinutes / 60)} machine hours are included each month, on every size.`;
}

/**
 * What the hours actually mean — the owner's "just loading up vs not".
 *
 * ── Every claim here was read out of the enforcement code ─────────────
 *
 * "A machine uses its hours whenever it's connected" —
 * `ConnectionBillingSweeper` bills each CONNECTED managed daemon every 30s
 * "whether or not it did any work". Idle-but-connected is billable, and saying
 * only "17 hours included" invites a user to assume otherwise.
 *
 * "Startup time isn't counted" — a daemon cannot appear in the liveness cache
 * until the gateway accepts its stream, so image pull and VM boot are never
 * billed. Worth stating because the previous sentence sounds worse than the
 * policy actually is.
 *
 * "machines pause" — this is the part that must not be guessed at, and it is
 * NOT the free-tier suspension path. `enforcement.Check` returns early on
 * `isPaid(sub)`, so ENFORCEMENT_SUSPEND_ON_OVERAGE never touches a paying
 * subscriber. Paid plans are gated in `svcdaemon`: included minutes, then any
 * coupon grant, and then `if !sub.OverageEnabled` the request is DENIED with
 * "enable overage billing to continue". Subscriptions are created with
 * `OverageEnabled: false`, so pausing is the DEFAULT and being billed overage
 * is a deliberate opt-in.
 *
 * That is why this says "pause… unless you turn on overage billing" and not
 * "you'll be billed $0.004/min". The rate is real and worth showing, but a
 * user who reads it as automatic would expect a bill they will never get —
 * and, worse, would not understand why their machine stopped.
 */
export function PlanFinePrint({ plan }: { plan: ComputePlanOption }) {
  return (
    <ul className="space-y-1.5 text-xs leading-relaxed text-muted-foreground">
      <li>
        A machine uses its included hours whenever it&apos;s connected — whether
        or not you&apos;re actively working. Startup time isn&apos;t counted,
        and stopping a machine stops the clock.
      </li>
      <li>
        {plan.includedMinutes < 0 ? (
          <>Hours are unlimited on this plan.</>
        ) : plan.overageCentsPerMinute > 0 ? (
          <>
            If you use all {Math.round(plan.includedMinutes / 60)} hours, new
            machines pause until next month — unless you turn on overage
            billing, which keeps them running at{" "}
            {formatOverageRate(plan.overageCentsPerMinute)}. Overage is off
            unless you switch it on, and you can set a monthly cap.
          </>
        ) : (
          <>
            If you use all {Math.round(plan.includedMinutes / 60)} hours, new
            machines pause until next month.
          </>
        )}
      </li>
      <li>Change or cancel any time from billing settings.</li>
    </ul>
  );
}

/**
 * The overage rate, per hour rather than per minute.
 *
 * The catalog stores cents-per-minute, and the numbers are small enough that
 * printing them directly gives "$0.004/min" — which reads as noise and is hard
 * to compare against a monthly price. Hours are the unit the rest of this page
 * already uses for compute.
 */
function formatOverageRate(centsPerMinute: number): string {
  const centsPerHour = centsPerMinute * 60;
  return `${formatCentsAsDollars(Math.round(centsPerHour))}/hour`;
}
