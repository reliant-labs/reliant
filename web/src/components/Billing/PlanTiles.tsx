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

import { machinePricingFootnote } from "./daemonUsage";

/**
 * One purchasable plan, reduced to what these tiles render.
 *
 * The tile shows `label` rather than deriving one, because the callers choose
 * along different axes: the two size-led surfaces (onboarding, and the
 * settings Plans tab) present ONE plan per machine size, while the mobile
 * screen lists the catalog by plan name. Deriving the label here would force
 * one of them to lie about what the user is picking.
 */
export interface ComputePlanOption {
  planId: string;
  label: string;
  /**
   * What this row buys, beneath its name — "4 GB RAM · 1 CPU".
   *
   * A real slot rather than something the caller concatenates into `label`.
   * Onboarding used to build `"Medium — 4 GB RAM · 1 CPU"` and then had to
   * read its coupon-coverage sentence from `size` instead, with a comment
   * explaining why, because the one string it had could not be un-concatenated
   * back into a bare machine name. Two facts, two fields.
   */
  detail?: string;
  /** The machine size this tile represents, when the caller selects by size. */
  size?: DaemonSizeName;
  monthlyPriceCents: number;
  /** -1 means unlimited; the tile says so rather than printing a number. */
  includedMinutes: number;
  /** Per-minute overage rate, in cents. 0 when the plan defines none. */
  overageCentsPerMinute: number;
  /** Hours this plan includes at its own size ("160"), for the picker footnote. Never printed on the row. */
  includedHours?: string | null;
}

export function PlanTiles({
  plans,
  loading,
  selectedPlanId,
  onSelect,
  coveredPlanId,
  currentPlanId,
}: {
  plans: ComputePlanOption[];
  loading?: boolean;
  selectedPlanId: string | undefined;
  onSelect: (option: ComputePlanOption) => void;
  /**
   * The plan this user is ALREADY subscribed to, marked as such and not
   * offered as a purchase.
   *
   * A different fact from `coveredPlanId` below and deliberately not merged
   * with it: "covered" means a grant pays for this machine's TIME right now,
   * "current" means this is the subscription being renewed each month. A user
   * switching plans needs to see which row they are on, and must not be able
   * to buy it again.
   */
  currentPlanId?: string | null;
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
          current={plan.planId === currentPlanId}
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
  current,
  onSelect,
}: {
  plan: ComputePlanOption;
  selected: boolean;
  covered?: boolean;
  /** The user already subscribes to this plan — see PlanTiles' currentPlanId. */
  current?: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      // Nothing to buy on the plan you are already on. Disabled rather than
      // hidden: a user comparing machines needs to see which one they have,
      // and removing the row would make the list they are choosing from
      // silently different from the list of machines.
      disabled={current}
      onClick={onSelect}
      className={cn(
        "flex w-full items-center justify-between gap-4 rounded-lg border px-4 py-3 text-left transition-colors",
        current
          ? "cursor-default border-primary/60 bg-primary/5"
          : selected
            ? "border-primary bg-primary/10"
            : "border-border bg-background hover:border-primary/40 hover:bg-muted/50",
      )}
    >
      <span className="min-w-0">
        <span className="flex items-center gap-2 text-sm font-semibold text-foreground">
          {plan.label}
          {(selected || current) && (
            <CheckCircle2 className="h-4 w-4 text-primary" />
          )}
        </span>
        {/* The specs, in their own slot. Callers pass a `detail` rather than
            folding it into `label`, so the bare machine name stays available
            to the prose that needs it. */}
        {plan.detail && (
          <span className="block text-xs text-muted-foreground">
            {plan.detail}
          </span>
        )}
      </span>
      <span className="flex-shrink-0 text-sm font-semibold text-foreground">
        {current ? (
          <span className="rounded-full bg-primary/10 px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider text-primary">
            Current plan
          </span>
        ) : covered ? (
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

/** Hours every offered plan includes at its own size, or null when they differ or are unknown. */
export function commonPlanHours(plans: ComputePlanOption[]): string | null {
  const hours = new Set(plans.map((p) => p.includedHours ?? null));
  const [only] = [...hours];
  return hours.size === 1 ? (only ?? null) : null;
}

/** The one footnote beneath every machine picker. */
export function MachinePricingLine({
  hours,
  testId = "machine-pricing-line",
}: {
  hours: string | null;
  testId?: string;
}) {
  return (
    <p className="mt-2 text-xs text-muted-foreground" data-testid={testId}>
      {machinePricingFootnote(hours)}
    </p>
  );
}
