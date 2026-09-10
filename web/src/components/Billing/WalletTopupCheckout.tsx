/**
 * Our own top-up page. Replaces `EmbeddedCheckoutPanel` for wallet credit.
 *
 * ── What changed, and what deliberately did not ───────────────────────
 *
 * What is gone is `EmbeddedCheckout` — Stripe's ENTIRE checkout page inside one
 * iframe, with their layout, their heading, their button and their spacing. The
 * page around the card fields is now ours: the amount selector, the copy, the
 * coupon path, the submit button, and every loading, error and success state.
 *
 * What did NOT change is where a card number lives. `<PaymentElement>` renders
 * per-field iframes served by Stripe; the PAN never touches a DOM node this
 * application controls, so PCI scope stays SAQ-A exactly as it was. A
 * hand-rolled `<input>` for a card number would move us to SAQ-A-EP — quarterly
 * scans and a materially heavier audit — which is why "our own page" means
 * Elements and never a card form we built.
 *
 * ── Apple Pay / Google Pay come for free here ─────────────────────────
 *
 * They are NOT a separate Payment Request Button. `<PaymentElement>` renders
 * the wallets itself when the account has them enabled and the domain is
 * registered — the same domain registration embedded checkout already needed,
 * so this is not a new cost and there is no extra component. They appear above
 * the card fields in a browser that offers them and are simply absent in one
 * that does not, which is also why there is no static row of payment logos:
 * availability is decided by the browser and the Dashboard, not by us.
 *
 * ── The rule that must not be relaxed ─────────────────────────────────
 *
 * `stripe.confirmPayment` resolving with `succeeded` is a PRESENTATION signal,
 * exactly as `EmbeddedCheckout`'s `onComplete` was. Entitlement is granted by
 * the `payment_intent.succeeded` webhook reaching our server, and the two can
 * be seconds apart — the second one can fail. So a successful confirmation
 * licenses "Confirming your payment…" and a poll, and nothing else. Success is
 * claimed only once `confirmSettlement` — the caller's own read of the SERVER —
 * says the money landed.
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
import {
  TOPUP_PRESETS_CENTS,
  formatCentsAsDollars,
} from "@/components/Settings/cloud/billingUtils";
import type { RedeemCouponResult } from "@/services/controlPlane/reliantAI";
import { cn } from "@/lib/utils";

import { useWalletTopupQuote } from "@/hooks/useCloudBillingQueries";

import { getStripe, isStripeConfigured } from "./stripe";
import { useWalletTopupIntent } from "./useWalletTopupIntent";
// The amount selector moved out for the same reason the plan tiles did: the
// onboarding model step now asks how much credit to buy WITHOUT a card.
import { CreditAmountPicker } from "./CreditAmountPicker";

/** How long to wait for the webhook before saying we could not confirm. */
const SETTLE_TIMEOUT_MS = 60_000;
const SETTLE_POLL_MS = 2_000;

export interface EntitlementLeg {
  /** Short label, e.g. "AI credit" or "Cloud machine". */
  label: string;
  /** What this leg buys, in the user's terms. */
  detail: string;
  /** Already paid for — by a coupon, an earlier purchase, or a subscription. */
  covered: boolean;
  /** Where the coverage came from, shown when covered. e.g. "coupon applied". */
  coveredBy?: string;
}

export interface WalletTopupCheckoutProps {
  /**
   * Re-read the SERVER and report whether the credit has landed.
   *
   * Supplied by the caller rather than read here, because the two surfaces own
   * different queries — settings has a wallet overview, onboarding has its
   * facts — and this component has no business knowing which. It is also the
   * only thing standing between "Stripe said yes" and this page claiming
   * success, so it must be a server read and never a local flag.
   */
  confirmSettlement: () => Promise<boolean>;
  /** Called once the purchase is settled server-side, or dismissed. */
  onDone: () => void;
  /**
   * Both legs of what the user is buying, in order. Rendered whole — including
   * legs already covered, marked as such — so a coupon's value stays visible
   * rather than silently vanishing from the page it paid for.
   */
  legs?: EntitlementLeg[];
  /** Rendered when the user must link an identity before purchasing. */
  renderIdentityRequired?: (message: string) => React.ReactNode;
  /** Seed amount; the user can change it. */
  defaultAmountCents?: number;
  className?: string;
}

export function WalletTopupCheckout({
  confirmSettlement,
  onDone,
  legs,
  renderIdentityRequired,
  defaultAmountCents = TOPUP_PRESETS_CENTS[1],
  className,
}: WalletTopupCheckoutProps) {
  const [amountCents, setAmountCents] = useState<number>(defaultAmountCents);
  const [settled, setSettled] = useState(false);

  // A coupon can pay for this leg outright, which is the whole reason the form
  // below sits beside the card and not under a "have a code?" footnote.
  const handleRedeemed = useCallback(
    async (_result: RedeemCouponResult) => {
      // Same gate as a card payment: the server is asked whether the credit
      // actually landed. A redemption that reported success but did not fund
      // the wallet must not advance the user any more than a failed card would.
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
          Your credit is on your account.
        </Notice>
      </div>
    );
  }

  return (
    <div className={cn("space-y-6", className)}>
      {legs && legs.length > 0 && <EntitlementSummary legs={legs} />}

      <section className="space-y-3">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          How much credit?
        </h3>
        <CreditAmountPicker value={amountCents} onChange={setAmountCents} />
        <p className="text-xs text-muted-foreground">
          Passthrough billing to the underlying API provider at no markup.
        </p>
        <TopupCostBreakdown creditCents={amountCents} />
      </section>

      <section className="space-y-3">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Pay with card
        </h3>
        <TopupPaymentForm
          // Re-keyed on the amount: a new amount is a new PaymentIntent, and
          // Elements cannot be handed a different client secret in place.
          key={amountCents}
          amountCents={amountCents}
          confirmSettlement={confirmSettlement}
          onSettled={() => setSettled(true)}
          renderIdentityRequired={renderIdentityRequired}
        />
      </section>

      <section className="space-y-2 border-t border-border pt-5">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Or use a code
        </h3>
        <p className="text-xs text-muted-foreground">
          Have a coupon? Redeem it here — it covers this in full, and no card is
          needed.
        </p>
        {/* "open", not "collapsed": on this page redeeming is a first-class way
            to pay, not an afterthought hidden behind a link. Stripe's own promo
            field could not do this — it knows Stripe coupons, not our grants. */}
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
 * Both legs of the purchase, including the ones already paid for.
 *
 * Showing a covered leg rather than hiding it is a deliberate choice: a user
 * who has just redeemed a compute coupon and is now being asked for money needs
 * to see WHAT the coupon covered, or the page reads as though the code did
 * nothing and they are paying for everything anyway.
 */
/**
 * What this top-up costs, itemised, before any card is entered.
 *
 * Three lines rather than one total, because the two numbers are genuinely
 * different things and the user is entitled to see both: the credit is what
 * lands in their balance, the fee is what card processing costs, and only the
 * sum reaches their statement. A page that showed just "$26.25" next to a "$25"
 * button is where chargebacks come from.
 *
 * EVERY FIGURE IS THE SERVER'S. `useWalletTopupQuote` returns what the backend
 * derived from the same function that builds the Stripe charge, so this cannot
 * quote one number and charge another. Nothing here multiplies by a percentage.
 *
 * Renders nothing while the quote is in flight rather than flashing a zero fee:
 * a fee that appears a moment after the total reads as a change to the price.
 */
function TopupCostBreakdown({ creditCents }: { creditCents: number }) {
  const quote = useWalletTopupQuote(creditCents);
  if (!quote.data) return null;

  const fee = Number(quote.data.feeCents);
  const total = Number(quote.data.totalCents);
  const percent = Number(quote.data.feePercent);

  return (
    <dl
      data-testid="topup-cost-breakdown"
      className="space-y-1.5 rounded-lg border border-border bg-muted/30 px-4 py-3 text-sm"
    >
      <div className="flex items-center justify-between">
        <dt className="text-muted-foreground">Credit to your balance</dt>
        <dd className="font-medium text-foreground">
          {formatCentsAsDollars(Number(quote.data.creditCents))}
        </dd>
      </div>
      {fee > 0 && (
        <div className="flex items-center justify-between">
          <dt className="text-muted-foreground">
            Processing fee{percent > 0 ? ` (${percent}%)` : ""}
          </dt>
          <dd className="font-medium text-foreground">
            {formatCentsAsDollars(fee)}
          </dd>
        </div>
      )}
      <div className="flex items-center justify-between border-t border-border pt-1.5">
        <dt className="font-medium text-foreground">Total charged</dt>
        <dd
          className="font-semibold text-foreground"
          data-testid="topup-total-charged"
        >
          {formatCentsAsDollars(total)}
        </dd>
      </div>
    </dl>
  );
}

function EntitlementSummary({ legs }: { legs: EntitlementLeg[] }) {
  return (
    <ul className="space-y-2">
      {legs.map((leg) => (
        <li
          key={leg.label}
          className={cn(
            "flex items-start gap-3 rounded-lg border px-4 py-3",
            leg.covered
              ? "border-primary/40 bg-primary/5"
              : "border-border bg-background",
          )}
        >
          {leg.covered ? (
            <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
          ) : (
            <span
              aria-hidden
              className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-muted-foreground/50"
            />
          )}
          <span className="min-w-0 flex-1">
            <span className="flex flex-wrap items-center gap-2 text-sm font-medium text-foreground">
              {leg.label}
              {leg.covered && (
                <span className="rounded-full bg-primary/10 px-2 py-0.5 text-2xs font-semibold text-primary">
                  {leg.coveredBy ?? "covered"}
                </span>
              )}
            </span>
            <span className="block text-xs text-muted-foreground">
              {leg.detail}
            </span>
          </span>
        </li>
      ))}
    </ul>
  );
}

/**
 * Mints the intent, then mounts Elements against it.
 *
 * `<Elements>` takes the client secret as a MOUNT-TIME option — it cannot be
 * swapped afterwards — so the provider is rendered only once a secret exists,
 * rather than mounted empty and updated. That is also why the parent re-keys
 * this component on the amount instead of letting the secret change underneath.
 */
function TopupPaymentForm({
  amountCents,
  confirmSettlement,
  onSettled,
  renderIdentityRequired,
}: {
  amountCents: number;
  confirmSettlement: () => Promise<boolean>;
  onSettled: () => void;
  renderIdentityRequired?: (message: string) => React.ReactNode;
}) {
  const intent = useWalletTopupIntent(BigInt(amountCents));

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

  if (intent.status === "identity_required") {
    return (
      <>
        {renderIdentityRequired ? (
          renderIdentityRequired(intent.message)
        ) : (
          <Notice tone="muted" icon={AlertCircle}>
            {intent.message}
          </Notice>
        )}
      </>
    );
  }

  if (intent.status === "error") {
    return (
      <Notice tone="error" icon={AlertCircle}>
        {intent.message}
      </Notice>
    );
  }

  if (intent.status === "creating") {
    return (
      <div className="flex items-center gap-2 px-1 py-6 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Preparing secure payment…
      </div>
    );
  }

  return (
    <Elements
      stripe={getStripe()}
      options={{
        clientSecret: intent.clientSecret,
        // Our page, our type scale. `appearance` restyles the fields INSIDE
        // Stripe's iframes, which is the only way to reach them — we cannot
        // (and must not) select into that DOM ourselves.
        appearance: { theme: "stripe", variables: { borderRadius: "8px" } },
      }}
    >
      <ConfirmPaymentForm
        amountCents={amountCents}
        confirmSettlement={confirmSettlement}
        onSettled={onSettled}
      />
    </Elements>
  );
}

/**
 * What happens between the user pressing Pay and us believing them.
 *
 * The states are enumerated rather than collapsed into pending/error, because
 * they mean genuinely different things to the person on the other side:
 * "your bank is asking you a question" is not "your card was declined", and
 * treating a 3DS challenge as a generic failure is precisely how a user ends up
 * believing they paid when they did not.
 */
type PayPhase =
  | { kind: "idle" }
  | { kind: "confirming" }
  | { kind: "authenticating" }
  | { kind: "settling" }
  | { kind: "settle_timeout" }
  | { kind: "failed"; message: string };

function ConfirmPaymentForm({
  amountCents,
  confirmSettlement,
  onSettled,
}: {
  amountCents: number;
  confirmSettlement: () => Promise<boolean>;
  onSettled: () => void;
}) {
  const stripe = useStripe();
  const elements = useElements();
  // The button must name what the CARD is charged, not the credit bought. It is
  // the last figure the user reads before paying, so it is the one that has to
  // match their statement.
  //
  // Without a quote there is no honest figure to put on it: the server adds the
  // fee regardless, so a button reading "Pay $25.00" would charge $26.25. The
  // form is disabled rather than labelled with a number we cannot stand behind
  // — see the same guard in OnboardingCheckout.
  const quote = useWalletTopupQuote(amountCents);
  const chargedCents = Number(quote.data?.totalCents ?? 0);
  const priced = Boolean(quote.data);
  const [phase, setPhase] = useState<PayPhase>({ kind: "idle" });

  // Guards the poll against a component that unmounted while it was running —
  // otherwise a settled payment calls setState on a dead tree, and worse, an
  // abandoned poll keeps running for its full minute.
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  /**
   * Wait for the SERVER to agree the money landed.
   *
   * The webhook is the only thing that credits a wallet, and it arrives on its
   * own schedule. Until it does, this page says it is confirming; if it never
   * does, it says that honestly rather than implying a success we cannot see.
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

    // `redirect: "if_required"` keeps a card payment — including its 3DS
    // challenge, which Stripe renders as a modal over our page — entirely in
    // place. Only a method that genuinely cannot complete without leaving
    // (a bank redirect) navigates, and then Stripe needs somewhere to return
    // to, which is this exact page.
    const { error, paymentIntent } = await stripe.confirmPayment({
      elements,
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
        // `processing` is not success — some methods settle asynchronously —
        // but in both cases the next authority is the webhook, so both wait.
        await waitForServer();
        return;
      case "requires_action":
        // Reached when the challenge could not be completed in place. Never
        // silently: a 3DS step that fails quietly leaves a user certain they
        // paid, which is the single worst outcome on this page.
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

  const busy =
    phase.kind === "confirming" ||
    phase.kind === "settling" ||
    phase.kind === "authenticating";

  return (
    <form onSubmit={(e) => void handleSubmit(e)} className="space-y-4">
      {/* Card fields, and Apple Pay / Google Pay where the browser offers
          them. Every input inside is a Stripe-hosted iframe.

          `terms.card: "never"` hides Stripe's own mandate paragraph, and no
          save checkbox is rendered at all: the intent is created with
          setup_future_usage, so the card is always kept. The one line below
          says so in our words, which is the disclosure that actually matters
          — a checkbox most people tick anyway is friction, not consent. */}
      <PaymentElement
        options={{
          layout: "tabs",
          terms: { card: "never" },
          wallets: { applePay: "auto", googlePay: "auto" },
        }}
      />
      <p className="text-xs text-muted-foreground">
        We&apos;ll securely save this card with Stripe so future top-ups are one
        click. You can remove it any time from the billing portal.
      </p>

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
        disabled={!stripe || busy || !priced}
        className={cn(
          "flex w-full items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-semibold transition-colors",
          !stripe || busy || !priced
            ? "cursor-not-allowed bg-muted text-muted-foreground"
            : "bg-primary text-primary-foreground hover:bg-primary/90",
        )}
      >
        {busy && <Loader2 className="h-4 w-4 animate-spin" />}
        {phase.kind === "settling"
          ? "Confirming…"
          : phase.kind === "confirming"
            ? "Processing…"
            : !priced
              ? "Working out the total…"
              : `Pay ${formatCentsAsDollars(chargedCents)}`}
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
