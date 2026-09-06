/**
 * The one billing moment in onboarding.
 *
 * ── What this replaces ────────────────────────────────────────────────
 *
 * Two steps used to eject the user to `/settings/billing`: `ComputeStep`'s
 * "Set up billing" button and `ModelStep`'s "Set up billing" link. Each was a
 * full navigation out of a wizard whose entire state lives in a URL search
 * param, and each existed for the same reason — the step could not proceed and
 * had nowhere to send the user. This step is where they both went.
 *
 * It is DERIVED, not routed to: `deriveStep` returns `'checkout'` exactly when
 * {@link requiresPayment} says money is owed, and never otherwise. A user on
 * local compute with their own API key does not see it, does not skip past it,
 * and has no idea it exists.
 *
 * ── Two payments, two sessions, one step ──────────────────────────────
 *
 * A compute plan is `mode: subscription` against a Stripe price; AI credit is
 * `mode: payment` for a variable amount that lands in the wallet through a
 * DIFFERENT webhook. Stripe cannot carry both in one Checkout Session while
 * keeping both webhook paths intact, so a user who owes both pays twice —
 * sequentially, in this step, with Stripe offering the saved card for the
 * second. What they never do is leave the flow.
 *
 * Which leg is showing is DERIVED from `requiresPayment` against freshly
 * refetched facts, not from a phase counter. When the compute subscription
 * lands, `computeEligible` flips and the requirement recomputes to credit-only
 * on its own; there is no state machine to get out of step with the server.
 *
 * ── The rule that shapes everything else ──────────────────────────────
 *
 * Entitlement is webhook-driven, so nothing here claims a purchase the server
 * has not confirmed. `EmbeddedCheckoutPanel.onDone` already means "the server
 * agrees", and this step still re-reads eligibility and the wallet balance
 * afterwards before writing `paid` — because those are the two facts
 * `requiresPayment` reads, and a `paid: true` written against stale facts
 * derives the user straight back here.
 *
 * And nothing that creates, cancels or charges fires from an effect. The
 * commit runs from the confirmation handler, once.
 */
import { useCallback, useMemo, useState } from "react";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";

import { CheckoutPanelWithIdentity } from "@/components/Billing/CheckoutPanelWithIdentity";
import type { CheckoutRequest } from "@/components/Billing/EmbeddedCheckoutPanel";
import { isSafeReturnTo } from "@/lib/returnTo";
import { RedeemCouponForm } from "@/components/RedeemCouponForm";
import {
  DAEMON_SIZE_ORDER,
  TOPUP_PRESETS_CENTS,
  derivePlanDisplay,
  formatCentsAsDollars,
  formatSizeLabel,
  isPurchasableComputePlan,
  smallestPlanAllowingSize,
  sortPlansForDisplay,
  type DaemonSizeName,
} from "@/components/Settings/cloud/billingUtils";
import { usePlans } from "@/hooks/useCloudBillingQueries";
import { cn } from "@/lib/utils";
import { trackEvent } from "@/lib/analytics";

import { ensureCommitKey } from "../commitLaunchPlan";
import { requiresPayment } from "../requiresPayment";
import type { PaymentFacts } from "../requiresPayment";
import { isCloudCompute } from "../types";
import { useCommitLaunchPlan } from "../useCommitLaunchPlan";
import { useOnboardingFacts } from "../useOnboardingFacts";
import type { LaunchPlan, StepProps } from "../types";

/**
 * How long to wait for the webhook to move the facts we read.
 *
 * The panel confirms a compute purchase against the SUBSCRIPTION query; this
 * step routes on the ELIGIBILITY query, and a wallet top-up has no
 * subscription to confirm against at all. Both gaps are the same wait, so
 * there is one poll rather than two, matching the panel's own 2s/60s shape.
 */
const SETTLE_POLL_MS = 2_000;
const SETTLE_TIMEOUT_MS = 60_000;

/** The default top-up: the smallest preset that buys a meaningful amount of
 *  work rather than the smallest one Stripe will accept. */
const DEFAULT_CREDIT_CENTS = TOPUP_PRESETS_CENTS[1];

/**
 * Where an OAuth identity-link should land the user: exactly where they are
 * standing now.
 *
 * The wizard's entire state — every answer, the commit key, the chosen plan —
 * is the `plan` object serialized into this URL. Rebuilding a path by hand
 * would drop all of it and restart the user at step one. Read live rather than
 * captured at mount so a plan edited since still round-trips.
 *
 * Falls back to the app root if the URL is somehow not same-origin-relative,
 * which is the same guard every other returnTo in the app applies.
 */
function currentOnboardingUrl(): string {
  const here = `${window.location.pathname}${window.location.search}`;
  return isSafeReturnTo(here) ? here : "/";
}

export function CheckoutStep({ plan, updatePlan, onNext }: StepProps) {
  const facts = useOnboardingFacts();
  const plansQ = usePlans();
  const { runCommit } = useCommitLaunchPlan(updatePlan);

  const [settling, setSettling] = useState(false);
  const [settleTimedOut, setSettleTimedOut] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [chosenSize, setChosenSize] = useState<DaemonSizeName | null>(null);

  const requirement = requiresPayment(plan, facts);

  // Membership, order and price are all server facts. A client-side plan table
  // would be a second declaration of what a plan costs, sitting next to a real
  // card form.
  const computePlans = useMemo(
    () =>
      sortPlansForDisplay(
        (plansQ.data?.plans ?? []).filter(isPurchasableComputePlan),
      ),
    [plansQ.data],
  );

  // Which sizes to offer is the union of what the catalog sells, and plan and
  // size are ONE axis — picking a size picks the cheapest plan that runs it.
  const sizes = useMemo(() => {
    const offered = new Set<string>();
    for (const p of computePlans) {
      for (const s of p.structuredLimits?.allowedDaemonSizes ?? []) {
        offered.add(s.toLowerCase());
      }
    }
    return DAEMON_SIZE_ORDER.filter((s) => offered.has(s));
  }, [computePlans]);

  const activeSize: DaemonSizeName | null =
    chosenSize && sizes.includes(chosenSize) ? chosenSize : (sizes[0] ?? null);

  const selectedPlan = useMemo(() => {
    if (!activeSize) return undefined;
    // A plan already recorded in the URL wins — it survived a reload, and
    // re-deriving it from a size default would silently switch what the user
    // is part-way through paying for.
    const recorded = computePlans.find((p) => p.id === plan.computePlanId);
    if (recorded) return recorded;
    return smallestPlanAllowingSize(computePlans, activeSize);
  }, [activeSize, computePlans, plan.computePlanId]);

  const creditCents = plan.aiCreditCents ?? DEFAULT_CREDIT_CENTS;

  /**
   * What the panel is buying right now.
   *
   * Compute first: it is the slower webhook and the one the machine waits on.
   * `null` means there is nothing left to buy, which is the exit condition.
   */
  const request: CheckoutRequest | null = requirement.needsCompute
    ? selectedPlan
      ? { kind: "compute_plan", planId: selectedPlan.id }
      : null
    : requirement.needsCredit
      ? { kind: "wallet_topup", amountCents: BigInt(creditCents) }
      : null;

  /**
   * Finish: record the confirmed purchase and start the commit.
   *
   * Called from the confirmation handler, never from an effect. The commit is
   * deliberately NOT awaited — provisioning a machine takes minutes and the
   * user has project questions to answer while it boots. It is keyed on the
   * plan's `commitKey`, so the terminal step's own `runCommit` returns THIS
   * commit rather than starting a second one.
   *
   * Settlement is recorded PER LEG, and only for legs this plan could owe.
   * Writing one undifferentiated "paid" verdict is what let AI credit bought
   * on a local plan pay for a cloud subscription chosen afterwards — see
   * {@link LaunchPlan.computeSettled}. This runs only once the freshly-read
   * facts agree the debt is cleared, so each flag it writes is a fact the
   * server has confirmed, not a claim about one.
   */
  const finish = useCallback(
    async (settledPlan: Partial<LaunchPlan>) => {
      const commitKey = await ensureCommitKey(settledPlan, updatePlan);
      void runCommit({ ...settledPlan, commitKey });
      const settled: Partial<LaunchPlan> = {};
      if (isCloudCompute(settledPlan.compute)) settled.computeSettled = true;
      if (settledPlan.modelProvider === "reliant_credits") {
        settled.creditSettled = true;
      }
      await updatePlan(settled);
      onNext();
    },
    [onNext, runCommit, updatePlan],
  );

  /**
   * The gap between "the server took the money" and "the facts this step
   * routes on say so".
   *
   * Polls rather than assuming. On timeout it says so honestly and offers to
   * check again — it never writes `paid` against facts that never moved.
   */
  const settleAndAdvance = useCallback(async () => {
    setError(null);
    setSettleTimedOut(false);
    setSettling(true);
    const deadline = Date.now() + SETTLE_TIMEOUT_MS;
    try {
      for (;;) {
        let fresh: PaymentFacts;
        try {
          fresh = await facts.refetch();
        } catch {
          fresh = { computeEligible: false, walletFunded: false };
        }
        const still = requiresPayment(plan, fresh);
        if (!still.any) {
          await finish(plan);
          return;
        }
        // Something is still owed. If it is the OTHER leg, the requirement has
        // moved on and the next panel renders — that is not a timeout.
        if (still.needsCompute !== requirement.needsCompute) return;
        if (Date.now() >= deadline) {
          setSettleTimedOut(true);
          return;
        }
        await new Promise((resolve) => setTimeout(resolve, SETTLE_POLL_MS));
      }
    } finally {
      setSettling(false);
    }
  }, [facts, finish, plan, requirement.needsCompute]);

  const handlePanelDone = useCallback(() => {
    trackEvent("onboarding_checkout_confirmed", {
      leg: requirement.needsCompute ? "compute" : "credit",
    });
    void settleAndAdvance();
  }, [requirement.needsCompute, settleAndAdvance]);

  // Nothing is owed and yet we are rendered: derivation is about to move on.
  // Say so rather than showing an empty card.
  if (!requirement.any) {
    return (
      <div className="space-y-3 py-6 text-center" role="status">
        <Loader2 className="mx-auto h-6 w-6 animate-spin text-primary" />
        <p className="text-sm text-muted-foreground">
          You&apos;re all set — continuing.
        </p>
      </div>
    );
  }

  // Two-leg-ness is a property of the PLAN, not of what is outstanding right
  // now. Deriving it from `requirement` alone made the header read "Step 1 of
  // 2" and then, the instant the compute webhook landed, plain "AI credit" —
  // renumbering the flow under a user who is mid-payment. A plan that buys
  // both is a two-payment plan for as long as it is on screen.
  const bothLegs =
    isCloudCompute(plan.compute) && plan.modelProvider === "reliant_credits";
  const legLabel = requirement.needsCompute
    ? bothLegs
      ? "Step 1 of 2 — your machine"
      : "Your machine"
    : bothLegs
      ? "Step 2 of 2 — AI credit"
      : "AI credit";

  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          Set up billing
        </h2>
        <p className="mx-auto max-w-[52ch] text-sm leading-relaxed text-muted-foreground">
          {bothLegs
            ? "You chose a Reliant machine and Reliant's models. They are billed separately, so there are two payments — the card you enter first is offered again for the second."
            : requirement.needsCompute
              ? "A Reliant machine runs on a monthly plan. Pick the size you need."
              : "Reliant's models draw on credit in your account. Add some to get started."}
        </p>
      </div>

      <div className="mx-auto w-full max-w-[560px] space-y-5">
        <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {legLabel}
        </p>

        {requirement.needsCompute ? (
          <ComputePlanPicker
            sizes={sizes}
            loading={plansQ.isLoading}
            selectedPlanId={selectedPlan?.id}
            planFor={(size) => smallestPlanAllowingSize(computePlans, size)}
            onChoose={(size, planId) => {
              setChosenSize(size);
              // Recorded in the URL so the choice survives a reload
              // mid-payment. Changing it re-keys the panel, which expires the
              // session in flight and starts a new one.
              void updatePlan({ computePlanId: planId });
            }}
          />
        ) : (
          <CreditAmountPicker
            valueCents={creditCents}
            onChoose={(cents) => void updatePlan({ aiCreditCents: cents })}
          />
        )}

        {settling ? (
          <div
            className="flex items-center gap-2 rounded-md border border-primary/40 bg-primary/10 px-4 py-3"
            role="status"
          >
            <Loader2 className="h-4 w-4 shrink-0 animate-spin text-primary" />
            <p className="text-sm text-foreground">
              Confirming your payment… this usually takes a few seconds.
            </p>
          </div>
        ) : settleTimedOut ? (
          <div className="space-y-2">
            <div className="flex items-start gap-2 rounded-md border border-border bg-muted/40 px-4 py-3">
              <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
              <p className="text-sm text-foreground">
                Your payment went through, but we haven&apos;t been able to
                confirm it yet. It usually lands within a minute.
              </p>
            </div>
            <button
              type="button"
              onClick={() => void settleAndAdvance()}
              className="w-full rounded-lg border border-border/40 bg-background py-2.5 text-sm font-medium text-foreground transition-colors hover:bg-muted"
            >
              Check again
            </button>
          </div>
        ) : request ? (
          <CheckoutPanelWithIdentity
            // Re-keyed on what is being bought: changing the plan or the amount
            // must start a new session rather than leaving a form that no
            // longer matches the choice above it.
            key={
              request.kind === "compute_plan"
                ? request.planId
                : String(request.amountCents)
            }
            request={request}
            onDone={handlePanelDone}
            // OAuth-only: the email path links inside the modal, so onboarding
            // is never navigated away from. A provider round-trip genuinely
            // leaves, and the wizard's ENTIRE state is the `plan` object in the
            // URL — so the way back is the live URL, not a rebuilt one. A
            // fabricated path would return the user to an empty plan at step
            // one, having already answered everything.
            returnTo={currentOnboardingUrl()}
          />
        ) : (
          <p className="text-sm text-muted-foreground">
            {plansQ.isLoading
              ? "Loading plans…"
              : "No plans are available in this setup. Go back and choose to run on your own computer."}
          </p>
        )}

        {/* Redeeming enough flips the requirement to false and derivation
            advances the user with no button to press. */}
        <RedeemCouponForm
          variant="collapsed"
          size="sm"
          onRedeemed={() => void facts.refetch()}
        />

        {error && <p className="text-center text-xs text-destructive">{error}</p>}
      </div>
    </div>
  );
}

function ComputePlanPicker({
  sizes,
  loading,
  selectedPlanId,
  planFor,
  onChoose,
}: {
  sizes: DaemonSizeName[];
  loading: boolean;
  selectedPlanId: string | undefined;
  planFor: (size: DaemonSizeName) => { id: string; priceCents: bigint; structuredLimits?: unknown } | undefined;
  onChoose: (size: DaemonSizeName, planId: string) => void;
}) {
  if (loading) {
    return (
      <div className="flex items-center gap-2 px-1 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" /> Loading plans…
      </div>
    );
  }
  if (sizes.length === 0) return null;

  return (
    <div className="space-y-2">
      {sizes.map((size) => {
        const sizePlan = planFor(size);
        if (!sizePlan) return null;
        const display = derivePlanDisplay(sizePlan as never);
        const selected = sizePlan.id === selectedPlanId;
        return (
          <button
            key={size}
            type="button"
            aria-pressed={selected}
            onClick={() => onChoose(size, sizePlan.id)}
            className={cn(
              "flex w-full items-center justify-between gap-4 rounded-xl border-2 px-4 py-3 text-left transition-all",
              selected
                ? "border-primary bg-primary/10"
                : "border-border/50 bg-background hover:border-primary/40 hover:bg-muted/50",
            )}
          >
            <span className="min-w-0">
              <span className="flex items-center gap-2 text-sm font-semibold text-foreground">
                {formatSizeLabel(size)}
                {selected && <CheckCircle2 className="h-4 w-4 text-primary" />}
              </span>
              <span className="block text-xs text-muted-foreground">
                {display.includedMinutes < 0
                  ? "Unlimited hours included"
                  : `${Math.round(display.includedMinutes / 60)} hours included each month`}
              </span>
            </span>
            <span className="flex-shrink-0 text-sm font-semibold text-foreground">
              {display.monthlyPriceCents == null
                ? "—"
                : `${formatCentsAsDollars(display.monthlyPriceCents)}/mo`}
            </span>
          </button>
        );
      })}
    </div>
  );
}

function CreditAmountPicker({
  valueCents,
  onChoose,
}: {
  valueCents: number;
  onChoose: (cents: number) => void;
}) {
  return (
    <div className="grid grid-cols-4 gap-2">
      {TOPUP_PRESETS_CENTS.map((cents) => (
        <button
          key={cents}
          type="button"
          aria-pressed={cents === valueCents}
          onClick={() => onChoose(cents)}
          className={cn(
            "rounded-lg border-2 py-2.5 text-sm font-semibold transition-all",
            cents === valueCents
              ? "border-primary bg-primary/10 text-foreground"
              : "border-border/50 bg-background text-muted-foreground hover:border-primary/40",
          )}
        >
          {formatCentsAsDollars(cents)}
        </button>
      ))}
    </div>
  );
}
