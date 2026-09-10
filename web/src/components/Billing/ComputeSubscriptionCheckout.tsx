/**
 * Our own compute-plan checkout. Replaces `EmbeddedCheckoutPanel` for
 * subscriptions, as `WalletTopupCheckout` already did for wallet credit.
 *
 * ── What was wrong, and why it was ONE thing ──────────────────────────
 *
 * The old panel rendered `<EmbeddedCheckout>` — Stripe's entire hosted
 * checkout page inside a single iframe. Four separate complaints came from
 * that one fact:
 *
 *   - a white panel in a dark app, because the page inside the iframe was
 *     Stripe's and no `appearance` we passed could reach it;
 *   - "Pay by bank to get $5 cash back", a Stripe Link promotion we never
 *     wrote and could not remove;
 *   - a stream of cancelled `elements-inner-express-checkout` requests, the
 *     wallet iframe mounting and being torn down on every plan switch;
 *   - a second of latency per switch, because each one minted a NEW Checkout
 *     Session server-side and booted a fresh iframe to display it.
 *
 * All four are gone here, and the last two are gone for the same structural
 * reason: this form mounts ONCE.
 *
 * ── Deferred intent: why the form survives a plan switch ──────────────
 *
 * `<Elements>` takes its client secret as a MOUNT-TIME option, so the obvious
 * shape — fetch a secret per plan, hand it to Elements — forces a remount on
 * every switch. That is precisely the old behaviour, reproduced with nicer
 * styling.
 *
 * So no secret is fetched up front. Elements mounts in DEFERRED mode
 * (`mode: "subscription"` plus an amount), which renders a real card form
 * against no intent at all. Switching plans calls `elements.update({ amount })`
 * — an in-place, synchronous, network-free call. The card fields keep their
 * state, the express-checkout iframe is never torn down, and switching is
 * instant because nothing is created at Stripe until the user presses Pay.
 *
 * ── This is SAFER than what it replaces, not merely faster ────────────
 *
 * The old path created a Stripe object on every plan switch. This one creates
 * nothing until Pay, so a user who opens checkout, tries all four tiles and
 * closes the tab leaves no trace at Stripe at all.
 *
 * The subscription created at Pay is `incomplete` and grants nothing. It does
 * not cancel the plan the user is currently paying for either — superseding
 * rides the webhook that confirms payment, which is where it was moved after
 * the abandoned-checkout defect. Nothing here weakens that.
 *
 * ── The rule that must not be relaxed ─────────────────────────────────
 *
 * `stripe.confirmPayment` resolving with `succeeded` is a PRESENTATION signal,
 * exactly as `EmbeddedCheckout`'s `onComplete` was. Entitlement is granted by
 * the webhook reaching our server, and the two can be seconds apart — the
 * second one can fail. A successful confirmation licenses "Confirming your
 * payment…" and a poll, and nothing more. Success is claimed only once
 * `confirmSettlement` — the caller's own read of the SERVER — agrees.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Elements,
  PaymentElement,
  useElements,
  useStripe,
} from "@stripe/react-stripe-js";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";

import { RedeemCouponForm } from "@/components/RedeemCouponForm";
import { formatCentsAsDollars } from "@/components/Settings/cloud/billingUtils";
import {
  isCheckoutIdentityRequired,
  useCreateComputeSubscriptionIntent,
} from "@/hooks/useCloudBillingQueries";
import type { RedeemCouponResult } from "@/services/controlPlane/reliantAI";
import { cn } from "@/lib/utils";

import { getStripe, isStripeConfigured } from "./stripe";
import { checkoutAppearance } from "./stripeAppearance";
import { PlanFinePrint, PlanTiles, type ComputePlanOption } from "./PlanTiles";

/** How long to wait for the webhook before saying we could not confirm. */
const SETTLE_TIMEOUT_MS = 60_000;
const SETTLE_POLL_MS = 2_000;

// The tiles and the fine print now live in ./PlanTiles, because the onboarding
// compute step renders the same prices WITHOUT a card. Re-exported so the
// existing importers of this module keep working.
export type { ComputePlanOption };

export interface ComputeSubscriptionCheckoutProps {
  plans: ComputePlanOption[];
  selectedPlanId: string | undefined;
  onSelectPlan: (option: ComputePlanOption) => void;
  /** Re-read the SERVER and report whether the plan is active. */
  confirmSettlement: () => Promise<boolean>;
  onDone: () => void;
  /** Rendered when the user must link an identity before purchasing. */
  renderIdentityRequired?: (message: string) => React.ReactNode;
  loadingPlans?: boolean;
  className?: string;
}

export function ComputeSubscriptionCheckout({
  plans,
  selectedPlanId,
  onSelectPlan,
  confirmSettlement,
  onDone,
  renderIdentityRequired,
  loadingPlans,
  className,
}: ComputeSubscriptionCheckoutProps) {
  const [settled, setSettled] = useState(false);

  const selected =
    plans.find((p) => p.planId === selectedPlanId) ?? plans[0] ?? null;

  // A coupon can cover this outright, which is why the form sits beside the
  // card rather than under a "have a code?" footnote.
  const handleRedeemed = useCallback(
    async (_result: RedeemCouponResult) => {
      // Same gate as a card payment: the SERVER is asked whether the
      // entitlement actually landed. A redemption that reported success but
      // granted nothing must not advance the user any more than a decline.
      if (await confirmSettlement()) setSettled(true);
    },
    [confirmSettlement],
  );

  useEffect(() => {
    if (settled) onDone();
  }, [settled, onDone]);

  if (settled) {
    return (
      <div className={className}>
        <Notice tone="success" icon={CheckCircle2}>
          Your plan is active.
        </Notice>
      </div>
    );
  }

  return (
    /* ONE surface. The owner's note was that "the choices should be almost
       part of the checkout page" — so the tiles and the card form live inside
       a single bordered card, divided by rules rather than separated into two
       floating panels with different backgrounds. */
    <div
      className={cn(
        "overflow-hidden rounded-xl border border-border bg-card",
        className,
      )}
    >
      <section className="space-y-3 p-5">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Choose your machine
        </h3>
        <PlanTiles
          plans={plans}
          loading={loadingPlans}
          selectedPlanId={selected?.planId}
          onSelect={onSelectPlan}
        />
        {selected && <PlanFinePrint plan={selected} />}
      </section>

      <section className="space-y-3 border-t border-border p-5">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Pay with card
        </h3>
        {selected ? (
          <SubscriptionPaymentForm
            plan={selected}
            confirmSettlement={confirmSettlement}
            onSettled={() => setSettled(true)}
            renderIdentityRequired={renderIdentityRequired}
          />
        ) : (
          /* An empty catalog is OUR problem, and the copy has to say so.
             It once read "No plans are available in this setup. Go back and
             choose to run on your own computer." — which described a server
             misconfiguration in words that sound like a product limitation,
             and then asked the user to undo a correct choice to work around
             it. The real cause was a stale admin-server whose `planToProto`
             did not project `price_cents`, so every plan arrived unpriced and
             the purchasable filter emptied the list.

             The coupon section below stays mounted in this state on purpose:
             a code is the one instrument that still works when the catalog
             does not, so an empty catalog must not be the end of the road. */
          <p
            className="text-sm text-muted-foreground"
            data-testid="checkout-plans-unavailable"
          >
            {loadingPlans
              ? "Loading plans…"
              : "We couldn't load the plans just now — that's on our end, not your setup. Try again in a moment, or redeem a coupon code below."}
          </p>
        )}
      </section>

      {/* Beside the card, inside the same surface — not below a 600px iframe
          where it was easy to miss. A code is also the one instrument that
          still works when the plan catalog does not load. */}
      <section className="space-y-2 border-t border-border bg-muted/20 p-5">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Or use a code
        </h3>
        <p className="text-xs text-muted-foreground">
          Have a coupon? Redeem it here — it covers this in full, and no card is
          needed.
        </p>
        <RedeemCouponForm
          variant="open"
          size="sm"
          onRedeemed={(result) => void handleRedeemed(result)}
        />
      </section>
    </div>
  );
}

/**
 * The card form. Mounted ONCE, in deferred mode, and updated in place.
 *
 * `<Elements>` is keyed on nothing that changes with the plan, which is the
 * entire point: the plan lives in `options.amount`, and react-stripe-js
 * forwards a changed `options` to `elements.update()` rather than remounting.
 * Switching plans therefore costs one synchronous call and no network at all.
 */
function SubscriptionPaymentForm({
  plan,
  confirmSettlement,
  onSettled,
  renderIdentityRequired,
}: {
  plan: ComputePlanOption;
  confirmSettlement: () => Promise<boolean>;
  onSettled: () => void;
  renderIdentityRequired?: (message: string) => React.ReactNode;
}) {
  // Dev and any deployment without a publishable key: there is no Stripe.js to
  // load, so a card form would mount into a permanent spinner. Say so, and
  // leave the coupon path — which works without Stripe — as the way through.
  if (!isStripeConfigured()) {
    return (
      <Notice tone="muted" icon={AlertCircle}>
        Card payments aren&apos;t configured in this environment. You can still
        redeem a coupon code below.
      </Notice>
    );
  }

  return (
    <Elements
      stripe={getStripe()}
      options={{
        // Deferred mode: no client secret, so nothing has been created at
        // Stripe yet and switching plans cannot invalidate an intent.
        mode: "subscription",
        amount: plan.monthlyPriceCents,
        currency: "usd",
        // Card only. This is what keeps Link's "pay by bank to get $5 cash
        // back" promotion — and every redirect-based method — out of a form
        // that is meant to confirm in place.
        paymentMethodTypes: ["card"],
        appearance: checkoutAppearance(),
      }}
    >
      <ConfirmSubscriptionForm
        plan={plan}
        confirmSettlement={confirmSettlement}
        onSettled={onSettled}
        renderIdentityRequired={renderIdentityRequired}
      />
    </Elements>
  );
}

/**
 * What happens between the user pressing Pay and us believing them.
 *
 * Enumerated rather than collapsed into pending/error, because the states mean
 * genuinely different things to the person on the other side: "your bank is
 * asking you a question" is not "your card was declined", and treating a 3DS
 * challenge as a generic failure is how a user ends up believing they paid
 * when they did not.
 */
type PayPhase =
  | { kind: "idle" }
  | { kind: "confirming" }
  | { kind: "authenticating" }
  | { kind: "settling" }
  | { kind: "settle_timeout" }
  | { kind: "identity_required"; message: string }
  | { kind: "failed"; message: string };

function ConfirmSubscriptionForm({
  plan,
  confirmSettlement,
  onSettled,
  renderIdentityRequired,
}: {
  plan: ComputePlanOption;
  confirmSettlement: () => Promise<boolean>;
  onSettled: () => void;
  renderIdentityRequired?: (message: string) => React.ReactNode;
}) {
  const stripe = useStripe();
  const elements = useElements();
  const createIntent = useCreateComputeSubscriptionIntent();
  const [phase, setPhase] = useState<PayPhase>({ kind: "idle" });

  // Guards the poll against a component that unmounted while it was running —
  // otherwise a settled payment calls setState on a dead tree, and an
  // abandoned poll keeps running for its full minute.
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  // react-query hands back a fresh mutation object every render; read it
  // through a ref so the submit handler does not have to depend on it.
  const mutation = useRef(createIntent);
  mutation.current = createIntent;

  /**
   * Wait for the SERVER to agree the plan is active.
   *
   * The webhook is the only thing that grants a subscription, and it arrives
   * on its own schedule. Until it does, this page says it is confirming; if it
   * never does, it says so honestly rather than implying a success we cannot
   * see.
   */
  const waitForServer = useCallback(async () => {
    setPhase({ kind: "settling" });
    const deadline = Date.now() + SETTLE_TIMEOUT_MS;
    for (;;) {
      if (!alive.current) return;
      if (await confirmSettlement()) {
        if (alive.current) onSettled();
        return;
      }
      if (Date.now() >= deadline) {
        if (alive.current) setPhase({ kind: "settle_timeout" });
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, SETTLE_POLL_MS));
    }
  }, [confirmSettlement, onSettled]);

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!stripe || !elements) return;

    setPhase({ kind: "confirming" });

    // Deferred mode requires this before confirming: it validates the fields
    // and collects them, and it is what surfaces "your card number is
    // incomplete" as an inline field error rather than as a failed charge.
    const { error: submitError } = await elements.submit();
    if (submitError) {
      setPhase({
        kind: "failed",
        message:
          submitError.message ??
          "Please check the card details and try again.",
      });
      return;
    }

    // Only NOW does anything exist at Stripe. Every plan the user tried before
    // pressing Pay cost nothing and left nothing behind.
    let clientSecret: string;
    try {
      const response = await mutation.current.mutateAsync(plan.planId);
      if (!response.paymentClientSecret) {
        // A subscription that needed no payment — full-coverage coupon, or a
        // trial. Nothing to confirm, so go straight to waiting for the server
        // rather than confirming an intent that does not exist.
        await waitForServer();
        return;
      }
      clientSecret = response.paymentClientSecret;
    } catch (err) {
      // The anti-anonymous-purchase guarantee, surfaced rather than swallowed.
      // It fires before anything is created, which is the point: there is
      // nothing to clean up.
      if (isCheckoutIdentityRequired(err)) {
        setPhase({
          kind: "identity_required",
          message: (err as Error).message,
        });
        return;
      }
      setPhase({
        kind: "failed",
        message:
          err instanceof Error
            ? err.message.replace(/^\[[a-z_]+\]\s*/i, "")
            : "We couldn't start this payment. Please try again.",
      });
      return;
    }

    // `redirect: "if_required"` keeps a card payment — including its 3DS
    // challenge, which Stripe renders as a modal over our page — entirely in
    // place. Only a method that genuinely cannot complete without leaving
    // would navigate, and card-only means none can.
    const { error, paymentIntent } = await stripe.confirmPayment({
      elements,
      clientSecret,
      redirect: "if_required",
      confirmParams: { return_url: window.location.href },
    });

    if (error) {
      // card_error and validation_error carry messages written for end users;
      // every other type is an integration or network fault whose message is
      // written for us and must not be shown as though the user did something
      // wrong.
      const message =
        (error.type === "card_error" || error.type === "validation_error") &&
        error.message
          ? error.message
          : "We couldn't process that payment. Please try again, or use a different card.";
      setPhase({ kind: "failed", message });
      return;
    }

    switch (paymentIntent?.status) {
      case "succeeded":
      case "processing":
        // `processing` is not success, but in both cases the next authority is
        // the webhook, so both wait.
        await waitForServer();
        return;
      case "requires_action":
        // Reached when the challenge could not be completed in place. Never
        // silently: a 3DS step that fails quietly leaves a user certain they
        // paid, which is the worst outcome on this page.
        setPhase({ kind: "authenticating" });
        return;
      case "requires_payment_method":
        setPhase({
          kind: "failed",
          message:
            "That payment didn't go through. Please check the details or try a different card.",
        });
        return;
      default:
        setPhase({
          kind: "failed",
          message:
            "We couldn't confirm that payment. Please try again in a moment.",
        });
    }
  };

  if (phase.kind === "identity_required" && renderIdentityRequired) {
    return <>{renderIdentityRequired(phase.message)}</>;
  }

  const busy =
    phase.kind === "confirming" ||
    phase.kind === "settling" ||
    phase.kind === "authenticating";

  return (
    <form onSubmit={(e) => void handleSubmit(e)} className="space-y-4">
      {/* Card fields, and Apple Pay / Google Pay where the browser offers
          them. Every input inside is a Stripe-hosted iframe, so the card
          number never touches a DOM node this application controls and PCI
          scope stays SAQ-A. */}
      <PaymentElement
        options={{
          layout: "tabs",
          terms: { card: "never" },
          wallets: { applePay: "auto", googlePay: "auto" },
        }}
      />
      <p className="text-xs text-muted-foreground">
        This is a monthly subscription. We&apos;ll securely save this card with
        Stripe to renew it, and you can change or cancel any time from billing
        settings.
      </p>

      {phase.kind === "identity_required" && (
        <Notice tone="muted" icon={AlertCircle}>
          {phase.message}
        </Notice>
      )}

      {phase.kind === "failed" && (
        <Notice tone="error" icon={AlertCircle}>
          {phase.message}
        </Notice>
      )}

      {phase.kind === "authenticating" && (
        <Notice tone="muted" icon={AlertCircle}>
          Your bank needs to verify this payment and the check didn&apos;t
          finish. Nothing has been charged — press Pay to try again.
        </Notice>
      )}

      {phase.kind === "settling" && (
        <Notice tone="pending" icon={Loader2} spin>
          Confirming your payment… this usually takes a few seconds.
        </Notice>
      )}

      {phase.kind === "settle_timeout" && (
        <Notice tone="muted" icon={AlertCircle}>
          Your payment went through, but we haven&apos;t been able to confirm it
          yet. It usually lands within a minute — refresh to check, and contact
          support if it doesn&apos;t.
        </Notice>
      )}

      <button
        type="submit"
        disabled={!stripe || busy}
        className={cn(
          "flex w-full items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-semibold transition-colors",
          !stripe || busy
            ? "cursor-not-allowed bg-muted text-muted-foreground"
            : "bg-primary text-primary-foreground hover:bg-primary/90",
        )}
      >
        {busy && <Loader2 className="h-4 w-4 animate-spin" />}
        {phase.kind === "settling"
          ? "Confirming…"
          : phase.kind === "confirming"
            ? "Processing…"
            : `Subscribe — ${formatCentsAsDollars(plan.monthlyPriceCents)}/mo`}
      </button>
    </form>
  );
}

function Notice({
  tone,
  icon: Icon,
  spin,
  children,
}: {
  tone: "error" | "muted" | "pending" | "success";
  icon: typeof AlertCircle;
  spin?: boolean;
  children: React.ReactNode;
}) {
  const tones = {
    error: "border-destructive/40 bg-destructive/10 text-destructive",
    muted: "border-border bg-muted/40 text-foreground",
    pending: "border-primary/40 bg-primary/10 text-foreground",
    success: "border-primary/40 bg-primary/10 text-foreground",
  } as const;
  const iconTones = {
    error: "text-destructive",
    muted: "text-muted-foreground",
    pending: "text-primary",
    success: "text-primary",
  } as const;

  return (
    <div
      role="status"
      className={cn(
        "flex items-start gap-2 rounded-md border px-4 py-3",
        tones[tone],
      )}
    >
      <Icon
        className={cn(
          "mt-0.5 h-4 w-4 shrink-0",
          iconTones[tone],
          spin && "animate-spin",
        )}
      />
      <p className="text-sm">{children}</p>
    </div>
  );
}
