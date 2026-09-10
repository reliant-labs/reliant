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
 * has not confirmed. Each checkout's `onDone` already means "the server
 * agrees", and this step still re-reads eligibility and the wallet balance
 * afterwards before writing `paid` — because those are the two facts
 * `requiresPayment` reads, and a `paid: true` written against stale facts
 * derives the user straight back here.
 *
 * And nothing that creates, cancels or charges fires from an effect. The
 * commit runs from the confirmation handler, once.
 */
import { useCallback, useMemo, useState } from "react";
import { AlertCircle, Loader2 } from "lucide-react";

import { LinkIdentityModal } from "@/components/Billing/LinkIdentityModal";
import {
  OnboardingCheckout,
  type CheckoutLine,
} from "@/components/Billing/OnboardingCheckout";
import { isSafeReturnTo } from "@/lib/returnTo";
import { capabilities } from "@/services/controlPlane/capabilities";
import {
  TOPUP_PRESETS_CENTS,
  derivePlanDisplay,
  isPurchasableComputePlan,
  sortPlansForDisplay,
} from "@/components/Settings/cloud/billingUtils";
import { usePlans } from "@/hooks/useCloudBillingQueries";
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
  // Bumped by a successful identity link, so the checkout remounts and mints a
  // fresh intent — nothing else about the purchase changed, so nothing else
  // would make it retry.
  const [linkAttempt, setLinkAttempt] = useState(0);

  const requirement = requiresPayment(plan, facts);

  // Membership, order and price are all server facts. A client-side plan table
  // would be a second declaration of what a plan costs.
  const computePlans = useMemo(
    () =>
      sortPlansForDisplay(
        (plansQ.data?.plans ?? []).filter(isPurchasableComputePlan),
      ),
    [plansQ.data],
  );

  /**
   * The plan the user chose ON THE COMPUTE STEP.
   *
   * There is no size picker here any more, and no fallback to "the smallest
   * plan we can find". The choice was made, with its price on screen, at the
   * moment the user decided to use a hosted machine; re-deriving it here would
   * be a second opinion about what they are buying.
   *
   * Absent means the user has no compute leg to pay for — either they chose
   * their own machine, or they were already entitled.
   */
  const selectedPlan = useMemo(
    () => computePlans.find((p) => p.id === plan.computePlanId),
    [computePlans, plan.computePlanId],
  );

  const creditCents = plan.aiCreditCents ?? DEFAULT_CREDIT_CENTS;

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
          // A failed refetch is pessimistic on the two SERVER reads only.
          // Billing availability is a build constant, so carrying it through
          // is not optimism: hardcoding it false here would report "nothing is
          // owed" on a deployment that genuinely bills, and finish() would
          // settle a debt the server never confirmed.
          fresh = {
            computeEligible: false,
            walletFunded: false,
            reliantBillingAvailable: capabilities.billing,
          };
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

  /**
   * Has the credit actually landed, according to the SERVER?
   *
   * This is what stands between Stripe's client-side "succeeded" and this step
   * believing a purchase happened. It re-reads the same wallet fact
   * `requiresPayment` routes on, so a confirmation that satisfies this cannot
   * derive the user straight back to the payment they just made.
   *
   * A failed refetch answers false rather than throwing: the caller polls, and
   * one bad read is a reason to look again, not to declare the payment lost.
   */
  const creditHasLanded = useCallback(async () => {
    try {
      return (await facts.refetch()).walletFunded;
    } catch {
      return false;
    }
  }, [facts]);

  /**
   * Has the compute plan actually landed, according to the SERVER?
   *
   * The compute counterpart of `creditHasLanded`, and it reads the SAME fact
   * this step routes on — eligibility, not the subscription query — so a
   * confirmation that satisfies it cannot derive the user straight back to the
   * payment they just made.
   */
  const computeHasLanded = useCallback(async () => {
    try {
      return (await facts.refetch()).computeEligible;
    } catch {
      return false;
    }
  }, [facts]);

  /**
   * Everything this plan buys, priced, in one summary.
   *
   * A leg already covered is SHOWN, marked covered, rather than dropped. The
   * alternative — render only what is outstanding — makes a redeemed compute
   * coupon disappear from the page at the exact moment the user is being asked
   * for money, so the code reads as though it did nothing. Cost is a few lines;
   * the confusion it prevents is a support ticket about a coupon that "didn't
   * work".
   */
  const lines = useMemo<CheckoutLine[]>(() => {
    const out: CheckoutLine[] = [];
    if (isCloudCompute(plan.compute)) {
      const display = derivePlanDisplay(selectedPlan);
      out.push({
        kind: "compute",
        label: "Cloud machine",
        detail: selectedPlan
          ? `${selectedPlan.name} — a Reliant-hosted machine to run your work on.`
          : "A Reliant-hosted machine to run your work on.",
        amountCents: display.monthlyPriceCents ?? 0,
        recurring: true,
        covered: !requirement.needsCompute,
        coveredBy: facts.computeEligible ? "already covered" : "paid",
      });
    }
    if (plan.modelProvider === "reliant_credits") {
      out.push({
        kind: "credit",
        label: "AI credit",
        detail: "Pays for Reliant's models as you use them. Never expires.",
        amountCents: creditCents,
        recurring: false,
        covered: !requirement.needsCredit,
        coveredBy: facts.walletFunded ? "already covered" : "paid",
      });
    }
    return out;
  }, [
    creditCents,
    facts.computeEligible,
    facts.walletFunded,
    plan.compute,
    plan.modelProvider,
    requirement.needsCompute,
    requirement.needsCredit,
    selectedPlan,
  ]);

  /**
   * A redeemed coupon finishes the same thing a payment finishes.
   *
   * This used to be `() => void facts.refetch()` — a refetch and nothing else
   * — while the Stripe path went through `settleAndAdvance`. So a code that
   * cleared the only outstanding leg left the user parked here reading
   * "Added 100 hours" with no button to press: the debt was gone, but no
   * settlement flag was written, no commit fired, and derivation was never
   * re-run against anything.
   *
   * Both events mean the same thing — the server granted entitlement and the
   * money question is closed — so both take the same path. Redemption is not a
   * second way to finish paying; it is the same finish reached by a different
   * instrument.
   *
   * It settles NO faster and on no weaker evidence than a card does. The
   * poll still re-reads eligibility and the wallet from the server and still
   * writes `computeSettled` / `creditSettled` only once they agree the debt is
   * cleared — a redeem call returning ok is not entitlement, and a flag
   * written from it would suppress the checkout requirement for money that
   * never moved. Provisioning is untouched either way: it happens at the
   * commit point, which asks the server for eligibility again before it will
   * start a machine.
   */
  const handleRedeemed = useCallback(() => {
    trackEvent("onboarding_checkout_confirmed", {
      leg: requirement.needsCompute ? "compute" : "credit",
      instrument: "coupon",
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

  const bothLegs =
    isCloudCompute(plan.compute) && plan.modelProvider === "reliant_credits";

  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          Confirm and pay
        </h2>
        <p className="mx-auto max-w-[52ch] text-sm leading-relaxed text-muted-foreground">
          {/* No more "there are two payments". There is one, because the two
              legs now bill the same Stripe customer and the second reuses the
              card the first collected. The header says what the user is about
              to do, and the summary below says exactly what for. */}
          {bothLegs
            ? "Here's everything you picked. One card covers both."
            : requirement.needsCompute
              ? "Here's the machine you picked."
              : "Here's the credit you picked."}
        </p>
      </div>

      <div className="mx-auto w-full max-w-[560px] space-y-5">
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
        ) : (
          /* ONE summary, ONE card, covering every leg still owed. The plan
             tiles and the credit amount are gone from this screen entirely —
             they were decided on the steps that asked for them, with their
             prices visible, before any card was involved. */
          <OnboardingCheckout
            key={`checkout:${linkAttempt}`}
            lines={lines}
            computePlanId={
              requirement.needsCompute ? plan.computePlanId : undefined
            }
            creditCents={requirement.needsCredit ? creditCents : undefined}
            confirmComputeSettled={computeHasLanded}
            confirmCreditSettled={creditHasLanded}
            onDone={handlePanelDone}
            onRedeemed={handleRedeemed}
            renderIdentityRequired={(message) => (
              <LinkIdentityModal
                message={message}
                returnTo={currentOnboardingUrl()}
                onLinked={() => setLinkAttempt((n) => n + 1)}
                onDismiss={() => undefined}
              />
            )}
          />
        )}

        {error && <p className="text-center text-xs text-destructive">{error}</p>}
      </div>
    </div>
  );
}
