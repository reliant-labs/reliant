/**
 * ONE card entry, covering everything onboarding owes.
 *
 * ── What this replaces ────────────────────────────────────────────────
 *
 * The checkout step used to mount two different components in sequence:
 * `ComputeSubscriptionCheckout` and then `WalletTopupCheckout`. Two card
 * forms, two submits, two "Confirming your payment…" waits — and, because the
 * two legs billed DIFFERENT Stripe customers, the copy promising "the card you
 * enter first is offered again for the second" could not be true. The user
 * genuinely typed a card twice.
 *
 * Now the decisions are made earlier — the compute step carries the plan tiles
 * and their prices, the model step carries the credit amount — so this page
 * has one job: show what is owed, take one card, and pay for all of it.
 *
 * ── Why both legs confirm ON-SESSION, rather than saving a card ───────
 *
 * The obvious shape is a SetupIntent: collect the card once, then charge both
 * legs off-session. It was rejected for a reason that matters more than the
 * elegance:
 *
 *   AN OFF-SESSION CHARGE HAS NO 3DS CHALLENGE AVAILABLE. There is nobody
 *   present to answer it, so a card that requires authentication comes back
 *   `requires_action` and the payment simply does not happen. On a card the
 *   user is standing in front of, that is a self-inflicted failure — and the
 *   remedy is to bring them back on-session with the client secret, i.e. to
 *   put them exactly where they already were.
 *
 * So both legs confirm in place, with the user present, and both keep the full
 * 3DS handling the two old components had. What makes it ONE card entry is not
 * a SetupIntent but the payment method itself: `confirmPayment` returns the
 * `payment_method` id it used, and the second leg is confirmed with that id
 * instead of a second `<PaymentElement>`. The card fields are mounted once and
 * submitted once; the second charge reuses what the first collected.
 *
 * This is only possible because both legs now bill the SAME Stripe customer —
 * see `getOrCreateComputeStripeCustomer`. A PaymentMethod attaches to exactly
 * one customer, so before that change this component could not have existed.
 *
 * ── The rule that must not be relaxed ─────────────────────────────────
 *
 * `confirmPayment` resolving with `succeeded` is a PRESENTATION signal.
 * Entitlement is granted by the webhook reaching our server, and the two can
 * be seconds apart — the second one can fail. A successful confirmation
 * licenses "Confirming your payment…" and a poll, and nothing more. Success is
 * claimed only once `confirmSettlement` — the caller's own read of the SERVER
 * — agrees.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Elements,
  PaymentElement,
  useElements,
  useStripe,
} from "@stripe/react-stripe-js";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";
import type { Stripe, StripeElements } from "@stripe/stripe-js";

import { RedeemCouponForm } from "@/components/RedeemCouponForm";
import { formatCentsAsDollars } from "@/components/Settings/cloud/billingUtils";
import {
  isCheckoutIdentityRequired,
  useCreateComputeSubscriptionIntent,
  useCreateWalletTopupPaymentIntent,
  useWalletTopupQuote,
} from "@/hooks/useCloudBillingQueries";
import { cn } from "@/lib/utils";

import { getStripe, isStripeConfigured } from "./stripe";
import { checkoutAppearance } from "./stripeAppearance";

/** How long to wait for the webhook before saying we could not confirm. */
const SETTLE_TIMEOUT_MS = 60_000;
const SETTLE_POLL_MS = 2_000;

/**
 * One thing the user is buying.
 *
 * A leg already covered is still LISTED, marked covered, rather than dropped:
 * a redeemed coupon that vanishes from the page reads as though it did
 * nothing, at the exact moment the user is being asked for money.
 */
export interface CheckoutLine {
  kind: "compute" | "credit";
  label: string;
  detail: string;
  /** What this line costs, in cents. Ignored when covered. */
  amountCents: number;
  /** Compute is a monthly subscription; credit is a one-off. */
  recurring: boolean;
  /** Already paid for — by a coupon, an earlier purchase, or a subscription. */
  covered: boolean;
  /** Where the coverage came from, shown when covered. */
  coveredBy?: string;
}

/**
 * How far each leg got.
 *
 * Enumerated per leg rather than as one verdict, because THE HARD CASE IS
 * PARTIAL SUCCESS: the subscription lands and the top-up declines, or the
 * reverse. A single "did it work" flag cannot say which of those happened, and
 * the difference decides both what the user is told and what a retry may
 * safely re-attempt.
 */
export type LegOutcome =
  | { status: "pending" }
  | { status: "settled" }
  | { status: "failed"; message: string }
  /** Confirmed at Stripe, but the webhook has not granted it yet. */
  | { status: "unconfirmed" };

export interface OnboardingCheckoutProps {
  lines: CheckoutLine[];
  /** The compute plan to subscribe to. Absent when compute is covered. */
  computePlanId?: string;
  /** Credit to buy, in cents. Absent when credit is covered. */
  creditCents?: number;
  /** Re-read the SERVER: has the compute plan landed? */
  confirmComputeSettled: () => Promise<boolean>;
  /** Re-read the SERVER: has the credit landed? */
  confirmCreditSettled: () => Promise<boolean>;
  /** Called once every owed leg is settled server-side. */
  onDone: () => void;
  /** A coupon cleared a leg; the caller re-reads the facts. */
  onRedeemed: () => void;
  renderIdentityRequired?: (message: string) => React.ReactNode;
  className?: string;
}

export function OnboardingCheckout(props: OnboardingCheckoutProps) {
  const { lines, className } = props;

  const owed = lines.filter((line) => !line.covered);

  /**
   * The processing fee, quoted BY THE SERVER.
   *
   * Fetched only when credit is actually owed: a coupon that covered the credit
   * leaves no card payment to process, so there is no processing cost to
   * recover and no fee to show. Compute alone is never fee'd either — see the
   * note on `creditOwed` below.
   *
   * The number is never computed here. `useWalletTopupQuote` returns what the
   * server derived from the same function that builds the Stripe charge, so the
   * figure on this page and the figure on the card statement come from one
   * place. A percentage applied in the browser would be a second implementation
   * of the rule, and one the user could edit.
   */
  const creditOwed = owed.find((line) => line.kind === "credit");
  const quoteQuery = useWalletTopupQuote(
    creditOwed?.amountCents ?? 0,
    Boolean(creditOwed),
  );
  const feeCents = creditOwed ? Number(quoteQuery.data?.feeCents ?? 0) : 0;
  const feePercent = Number(quoteQuery.data?.feePercent ?? 0);

  /**
   * Credit is owed but we could not price it — and that must BLOCK the payment.
   *
   * The fee is added SERVER-SIDE whether or not this page managed to fetch it.
   * So a checkout that renders a confident "Due today $45.00" on a failed quote
   * charges $46.25, which is precisely the undisclosed surprise this feature
   * exists to prevent: worse than showing nothing, because the wrong number
   * carries our authority.
   *
   * Failing closed costs an unavailable checkout during an outage. Failing open
   * costs a chargeback and a support ticket, and it does so silently. Observed
   * live: against a server that predated the quote RPC, this page showed
   * $45.00 for a purchase the backend would have charged $46.25 at.
   */
  const cannotPriceCredit = Boolean(creditOwed) && !quoteQuery.data;

  // The fee rides on top of the owed lines. Compute's monthly price is listed
  // on the plan tile and is charged as listed, so it contributes nothing here.
  const totalCents =
    owed.reduce((sum, line) => sum + line.amountCents, 0) + feeCents;

  /**
   * A machine is owed, but we do not know which plan to sell.
   *
   * The stale-binary shape: `planToProto` did not project `price_cents`, every
   * plan arrived unpriced, and the purchasable filter emptied the catalog — so
   * the compute step could offer no tile and recorded no choice. Charging is
   * impossible, and rendering a card form for a $0 total would be worse than
   * useless: the user would press Pay and hit an error from the intent call.
   *
   * The copy says the fault is OURS, because it is. It once read "No plans are
   * available in this setup. Go back and choose to run on your own computer."
   * — a server misconfiguration described as a product limitation, with advice
   * to undo a correct choice to work around it. The coupon section below stays
   * mounted, because a code is the one instrument that still works when the
   * catalog does not.
   *
   * ── Why the price, and not just the plan id, is the test ─────────────
   *
   * This used to be `!props.computePlanId` alone, and it fired on a state that
   * has nothing to do with the catalog: an ENTITLED user's compute leg carries
   * no plan id by design (`ComputeStep` deliberately records none — there is
   * nothing to buy), so a user who owed only AI credit was told we could not
   * load the machine plans. That is the defect the owner hit having brought
   * their own key with a compute coupon applied.
   *
   * A leg is unsellable when we cannot NAME a price for it. `amountCents <= 0`
   * is that condition directly — it is the same 0 the unpriced catalog
   * produces, and it is what `<Elements>` refuses to mount against.
   */
  const unsellableCompute = owed.find(
    (line) => line.kind === "compute" && (!props.computePlanId || line.amountCents <= 0),
  );

  /**
   * Nothing here can be charged, whatever the line items claim.
   *
   * THE CRASH THIS PREVENTS: `<Elements options={{ amount }}>` throws
   * `IntegrationError: amount must be greater than 0` synchronously out of
   * render, which no local error boundary catches — the entire app is replaced
   * by "Something went wrong" and the user's onboarding ends with no Back and
   * no coupon field. It fires on the happiest path there is, someone whose
   * coupon covered the bill.
   *
   * Gating on `owed.length` alone was not enough: a line can be owed and still
   * carry a zero amount (an unpriced plan), so the section opened and the
   * provider mounted against a $0 total. The guard belongs on the money.
   */
  const nothingToCharge = totalCents <= 0;

  return (
    <div
      className={cn(
        "overflow-hidden rounded-xl border border-border bg-card",
        className,
      )}
    >
      <section className="space-y-3 p-5">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          What you&apos;re getting
        </h3>
        <OrderSummary
          lines={lines}
          // A total we cannot vouch for is not shown at all. See
          // `cannotPriceCredit`: a confident wrong total is worse than none.
          totalCents={cannotPriceCredit ? 0 : totalCents}
          feeCents={feeCents}
          feePercent={feePercent}
        />
      </section>

      {owed.length > 0 && (
        <section className="space-y-3 border-t border-border p-5">
          <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
            Pay with card
          </h3>
          {cannotPriceCredit ? (
            <p
              className="text-sm text-muted-foreground"
              data-testid="checkout-price-unavailable"
            >
              We couldn&apos;t work out the total just now — that&apos;s on our
              end. Try again in a moment, or redeem a coupon code below.
            </p>
          ) : unsellableCompute || nothingToCharge ? (
            <p
              className="text-sm text-muted-foreground"
              data-testid="checkout-plans-unavailable"
            >
              We couldn&apos;t load the machine plans just now — that&apos;s on
              our end, not your setup. Try again in a moment, or redeem a coupon
              code below.
            </p>
          ) : (
            <SingleCardForm {...props} owed={owed} totalCents={totalCents} />
          )}
        </section>
      )}

      <section className="space-y-2 border-t border-border bg-muted/20 p-5">
        <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          Or use a code
        </h3>
        <p className="text-xs text-muted-foreground">
          Have a coupon? Redeem it here — it covers what it applies to in full,
          and no card is needed.
        </p>
        <RedeemCouponForm
          variant="open"
          size="sm"
          onRedeemed={() => props.onRedeemed()}
        />
      </section>
    </div>
  );
}

/**
 * Everything being bought, with the covered lines marked rather than hidden.
 *
 * The total counts only what is OWED, so a coupon visibly reduces it — which
 * is the whole point of leaving covered lines on the page.
 *
 * ── Why the fee is a LINE and not a footnote ─────────────────────────
 *
 * It is money the user is about to be charged that they did not choose, so it
 * gets the same visual weight as the things they did choose. A fee folded into
 * the credit line would misstate the credit; a fee mentioned only in prose
 * under the button is a fee people discover on their statement, and that is a
 * chargeback and a support ticket rather than a disclosure.
 *
 * It renders only when non-zero, so a couponed or compute-only checkout — both
 * of which involve no processing cost to recover — shows no fee line at all
 * rather than a $0.00 row inviting the question.
 */
function OrderSummary({
  lines,
  totalCents,
  feeCents,
  feePercent,
}: {
  lines: CheckoutLine[];
  totalCents: number;
  /** Server-quoted. Zero when nothing here attracts a fee. */
  feeCents: number;
  /** The disclosed rate, so the copy never hardcodes a percentage. */
  feePercent: number;
}) {
  return (
    <div className="space-y-2">
      <ul className="space-y-2">
        {lines.map((line) => (
          <li
            key={line.kind}
            data-testid={`checkout-line-${line.kind}`}
            className={cn(
              "flex items-start gap-3 rounded-lg border px-4 py-3",
              line.covered
                ? "border-primary/40 bg-primary/5"
                : "border-border bg-background",
            )}
          >
            {line.covered ? (
              <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
            ) : (
              <span
                aria-hidden
                className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-muted-foreground/50"
              />
            )}
            <span className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-2 text-sm font-medium text-foreground">
                {line.label}
                {line.covered && (
                  <span className="rounded-full bg-primary/10 px-2 py-0.5 text-2xs font-semibold text-primary">
                    {line.coveredBy ?? "covered"}
                  </span>
                )}
              </span>
              <span className="block text-xs text-muted-foreground">
                {line.detail}
              </span>
            </span>
            <span className="shrink-0 text-sm font-semibold text-foreground">
              {line.covered ? (
                <span className="text-muted-foreground line-through">
                  {formatCentsAsDollars(line.amountCents)}
                </span>
              ) : (
                <>
                  {formatCentsAsDollars(line.amountCents)}
                  {line.recurring && (
                    <span className="text-xs font-normal text-muted-foreground">
                      /mo
                    </span>
                  )}
                </>
              )}
            </span>
          </li>
        ))}

        {feeCents > 0 && (
          <li
            data-testid="checkout-line-fee"
            className="flex items-start gap-3 rounded-lg border border-border bg-background px-4 py-3"
          >
            <span
              aria-hidden
              className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-muted-foreground/50"
            />
            <span className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-2 text-sm font-medium text-foreground">
                Processing fee
                {feePercent > 0 && (
                  <span className="rounded-full bg-muted px-2 py-0.5 text-2xs font-semibold text-muted-foreground">
                    {feePercent}%
                  </span>
                )}
              </span>
              <span className="block text-xs text-muted-foreground">
                Covers card processing on the credit above. Your full credit
                lands in your balance.
              </span>
            </span>
            <span className="shrink-0 text-sm font-semibold text-foreground">
              {formatCentsAsDollars(feeCents)}
            </span>
          </li>
        )}
      </ul>

      {totalCents > 0 && (
        <div className="flex items-center justify-between border-t border-border px-4 pt-3 text-sm">
          <span className="font-medium text-foreground">Due today</span>
          <span
            className="font-semibold text-foreground"
            data-testid="checkout-total"
          >
            {formatCentsAsDollars(totalCents)}
          </span>
        </div>
      )}
    </div>
  );
}

/** What the page is doing between the user pressing Pay and us believing them. */
type PayPhase =
  | { kind: "idle" }
  | { kind: "confirming" }
  | { kind: "authenticating" }
  | { kind: "settling" }
  | { kind: "settle_timeout" }
  | { kind: "identity_required"; message: string }
  | { kind: "failed"; message: string };

function SingleCardForm(
  props: OnboardingCheckoutProps & {
    owed: CheckoutLine[];
    totalCents: number;
  },
) {
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

  // Belt and braces on the one error that takes down the whole app.
  //
  // The caller already refuses to render this for a non-positive total, so
  // reaching here with one is a bug upstream. It must still not be a CRASH:
  // `<Elements>` validates `amount` eagerly and throws out of render, past
  // every local boundary, and the user loses onboarding entirely. A guard that
  // costs one comparison buys immunity from every future caller that forgets.
  if (props.totalCents <= 0) {
    return (
      <Notice tone="muted" icon={AlertCircle}>
        There&apos;s nothing to pay for right now. If you were expecting a
        charge, try again in a moment or redeem a coupon code below.
      </Notice>
    );
  }

  return (
    <Elements
      stripe={getStripe()}
      options={{
        // Deferred mode: nothing is created at Stripe until Pay is pressed, so
        // a user who opens checkout and leaves has cost nothing and left no
        // trace. `payment` rather than `subscription` because this form may be
        // paying for a subscription, a one-off, or both at once — the mode
        // only shapes the mandate text Stripe would render, and we suppress
        // that in favour of our own wording below.
        mode: "payment",
        amount: props.totalCents,
        currency: "usd",
        // Card only. This is what keeps Link's "pay by bank to get $5 cash
        // back" promotion — and every redirect-based method — out of a form
        // meant to confirm in place. It also matters more here than it did in
        // the single-leg forms: a redirect mid-flow would lose the second leg.
        paymentMethodTypes: ["card"],
        appearance: checkoutAppearance(),
      }}
    >
      <ConfirmBothLegs {...props} />
    </Elements>
  );
}

function ConfirmBothLegs({
  owed,
  totalCents,
  computePlanId,
  creditCents,
  confirmComputeSettled,
  confirmCreditSettled,
  onDone,
  renderIdentityRequired,
}: OnboardingCheckoutProps & {
  owed: CheckoutLine[];
  totalCents: number;
}) {
  const stripe = useStripe();
  const elements = useElements();
  const createComputeIntent = useCreateComputeSubscriptionIntent();
  const createTopupIntent = useCreateWalletTopupPaymentIntent();

  const [phase, setPhase] = useState<PayPhase>({ kind: "idle" });
  /**
   * Per-leg progress, and the reason this component is not a boolean.
   *
   * A leg that reaches `settled` is NEVER re-attempted by a retry — that is
   * the entire double-charge guard on the client side, and it is why the
   * outcome is recorded per leg rather than thrown away when the other leg
   * fails.
   */
  const [outcomes, setOutcomes] = useState<Record<string, LegOutcome>>({});

  // react-query hands back a fresh mutation object every render; read them
  // through refs so the submit handler need not depend on them.
  const computeMutation = useRef(createComputeIntent);
  computeMutation.current = createComputeIntent;
  const topupMutation = useRef(createTopupIntent);
  topupMutation.current = createTopupIntent;

  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const needsCompute = owed.some((l) => l.kind === "compute");
  const needsCredit = owed.some((l) => l.kind === "credit");

  /**
   * Wait for the SERVER to agree a leg landed.
   *
   * Returns whether it settled within the budget. A timeout is NOT a failure:
   * the money moved and the webhook is late, which is a different thing from a
   * decline and must be said differently.
   */
  const waitForServer = useCallback(
    async (confirm: () => Promise<boolean>): Promise<boolean> => {
      const deadline = Date.now() + SETTLE_TIMEOUT_MS;
      for (;;) {
        if (!alive.current) return false;
        try {
          if (await confirm()) return true;
        } catch {
          // One bad read is a reason to look again, not to declare the
          // payment lost.
        }
        if (Date.now() >= deadline) return false;
        await new Promise((resolve) => setTimeout(resolve, SETTLE_POLL_MS));
      }
    },
    [],
  );

  /**
   * Pay for the compute subscription, and report the payment method used.
   *
   * The FIRST leg, always — it is the one that collects the card, and the
   * credit leg reuses what it returns. Ordering matters for a second reason
   * too: compute is the larger, recurring commitment, so if only one leg can
   * succeed it should be the one the user's machine depends on.
   */
  const payCompute = useCallback(
    async (
      stripeClient: Stripe,
      elementsInstance: StripeElements,
    ): Promise<
      | { ok: true; paymentMethodId: string | null }
      | { ok: false; phase: PayPhase }
    > => {
      let clientSecret: string;
      try {
        const response = await computeMutation.current.mutateAsync(
          computePlanId!,
        );
        if (!response.paymentClientSecret) {
          // A subscription with nothing to pay — full-coverage coupon, or a
          // trial. Nothing to confirm; the card was still collected and can
          // still pay the other leg, so report no payment method rather than
          // failing.
          return { ok: true, paymentMethodId: null };
        }
        clientSecret = response.paymentClientSecret;
      } catch (err) {
        if (isCheckoutIdentityRequired(err)) {
          return {
            ok: false,
            phase: { kind: "identity_required", message: (err as Error).message },
          };
        }
        return { ok: false, phase: { kind: "failed", message: startFailure(err) } };
      }

      const { error, paymentIntent } = await stripeClient.confirmPayment({
        elements: elementsInstance,
        clientSecret,
        redirect: "if_required",
        confirmParams: { return_url: window.location.href },
      });
      if (error) return { ok: false, phase: { kind: "failed", message: cardFailure(error) } };

      switch (paymentIntent?.status) {
        case "succeeded":
        case "processing":
          return {
            ok: true,
            paymentMethodId: paymentMethodIdOf(paymentIntent.payment_method),
          };
        case "requires_action":
          // The challenge could not be completed in place. Never silently: a
          // 3DS step that fails quietly leaves a user certain they paid.
          return { ok: false, phase: { kind: "authenticating" } };
        case "requires_payment_method":
          return {
            ok: false,
            phase: {
              kind: "failed",
              message:
                "That payment didn't go through. Please check the details or try a different card.",
            },
          };
        default:
          return {
            ok: false,
            phase: {
              kind: "failed",
              message:
                "We couldn't confirm that payment. Please try again in a moment.",
            },
          };
      }
    },
    [computePlanId],
  );

  /**
   * Pay for the credit, reusing the card the compute leg already collected.
   *
   * `paymentMethodId` is what makes this ONE card entry: when present, the
   * intent is confirmed against that saved method and no card fields are
   * mounted a second time. When absent — credit is the only owed leg — the
   * mounted Elements collect it, exactly as the old top-up form did.
   */
  const payCredit = useCallback(
    async (
      stripeClient: Stripe,
      elementsInstance: StripeElements,
      paymentMethodId: string | null,
    ): Promise<{ ok: true } | { ok: false; phase: PayPhase }> => {
      let clientSecret: string;
      try {
        const response = await topupMutation.current.mutateAsync(
          BigInt(creditCents!),
        );
        if (!response.paymentIntentClientSecret) {
          return {
            ok: false,
            phase: {
              kind: "failed",
              message: "We couldn't start this payment. Please try again.",
            },
          };
        }
        clientSecret = response.paymentIntentClientSecret;
      } catch (err) {
        if (isCheckoutIdentityRequired(err)) {
          return {
            ok: false,
            phase: { kind: "identity_required", message: (err as Error).message },
          };
        }
        return { ok: false, phase: { kind: "failed", message: startFailure(err) } };
      }

      const { error, paymentIntent } = paymentMethodId
        ? // The reuse. No Elements, no second card entry — Stripe charges the
          // method the first leg collected, and still raises a 3DS challenge
          // in place if the bank asks for one, because the user is present.
          await stripeClient.confirmPayment({
            clientSecret,
            redirect: "if_required",
            confirmParams: {
              payment_method: paymentMethodId,
              return_url: window.location.href,
            },
          })
        : await stripeClient.confirmPayment({
            elements: elementsInstance,
            clientSecret,
            redirect: "if_required",
            confirmParams: { return_url: window.location.href },
          });

      if (error) return { ok: false, phase: { kind: "failed", message: cardFailure(error) } };

      switch (paymentIntent?.status) {
        case "succeeded":
        case "processing":
          return { ok: true };
        case "requires_action":
          return { ok: false, phase: { kind: "authenticating" } };
        case "requires_payment_method":
          return {
            ok: false,
            phase: {
              kind: "failed",
              message:
                "That payment didn't go through. Please check the details or try a different card.",
            },
          };
        default:
          return {
            ok: false,
            phase: {
              kind: "failed",
              message:
                "We couldn't confirm that payment. Please try again in a moment.",
            },
          };
      }
    },
    [creditCents],
  );

  /**
   * One submit, both legs, and a retry that never re-charges a settled one.
   *
   * ── The partial-failure decision ──────────────────────────────────────
   *
   * Legs are paid SEQUENTIALLY and each is recorded the moment the server
   * confirms it. A retry re-reads `outcomes` and skips anything already
   * `settled`, so pressing Pay again after a half-failure charges only the leg
   * that did not land.
   *
   * That is deliberately not the only guard, because a client-side record is
   * lost on reload. The server holds the other two, and they are the ones that
   * actually matter:
   *
   *   - The compute leg is keyed at Stripe on
   *     `compute-subintent-<user>-<plan>-<hour>`, so a retry inside the hour
   *     REPLAYS the original subscription rather than creating a second one.
   *   - The credit leg reuses the pending `wallet_topups` row for an identical
   *     amount and keys the intent on `wallet-topup-intent-<row id>`, so a
   *     retry replays that intent instead of opening a second payment.
   *
   * So the worst case for a user who reloads mid-failure and pays again is
   * that Stripe returns the same objects — not a double charge.
   */
  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!stripe || !elements) return;

    setPhase({ kind: "confirming" });

    // Deferred mode requires this before confirming: it validates the fields
    // and is what surfaces "your card number is incomplete" as an inline field
    // error rather than as a failed charge.
    const { error: submitError } = await elements.submit();
    if (submitError) {
      setPhase({
        kind: "failed",
        message:
          submitError.message ?? "Please check the card details and try again.",
      });
      return;
    }

    let paymentMethodId: string | null = null;
    let timedOut = false;

    // ── Leg 1: the compute subscription ──────────────────────────────
    if (needsCompute && outcomes.compute?.status !== "settled") {
      const result = await payCompute(stripe, elements);
      if (!result.ok) {
        setPhase(result.phase);
        return;
      }
      paymentMethodId = result.paymentMethodId;

      setPhase({ kind: "settling" });
      if (await waitForServer(confirmComputeSettled)) {
        setOutcomes((prev) => ({ ...prev, compute: { status: "settled" } }));
      } else {
        // Paid, but the webhook has not granted it. NOT a failure — and
        // critically, not a reason to abandon the second leg: the card worked,
        // so the credit can still be bought while this one catches up.
        setOutcomes((prev) => ({ ...prev, compute: { status: "unconfirmed" } }));
        timedOut = true;
      }
    }

    // ── Leg 2: the wallet credit, on the card leg 1 collected ────────
    if (needsCredit && outcomes.credit?.status !== "settled") {
      const result = await payCredit(stripe, elements, paymentMethodId);
      if (!result.ok) {
        // THE PARTIAL FAILURE. The compute leg above may have succeeded, and
        // its outcome is already recorded — so the message names what DID
        // work, and a retry will not touch it.
        setPhase(
          result.phase.kind === "failed" && needsCompute
            ? {
                kind: "failed",
                message: `Your machine is paid for. The credit didn't go through: ${result.phase.message}`,
              }
            : result.phase,
        );
        return;
      }

      setPhase({ kind: "settling" });
      if (await waitForServer(confirmCreditSettled)) {
        setOutcomes((prev) => ({ ...prev, credit: { status: "settled" } }));
      } else {
        setOutcomes((prev) => ({ ...prev, credit: { status: "unconfirmed" } }));
        timedOut = true;
      }
    }

    if (!alive.current) return;
    if (timedOut) {
      setPhase({ kind: "settle_timeout" });
      return;
    }
    onDone();
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
          scope stays SAQ-A. Mounted ONCE, for both legs. */}
      <PaymentElement
        options={{
          layout: "tabs",
          terms: { card: "never" },
          wallets: { applePay: "auto", googlePay: "auto" },
        }}
      />
      <p className="text-xs text-muted-foreground">
        {needsCompute && needsCredit
          ? "One card, both purchases. We'll securely save it with Stripe to renew your monthly plan, and you can change or cancel any time from billing settings."
          : needsCompute
            ? "This is a monthly subscription. We'll securely save this card with Stripe to renew it, and you can change or cancel any time from billing settings."
            : "We'll securely save this card with Stripe so future top-ups are one click. You can remove it any time from the billing portal."}
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
          yet. It usually lands within a minute — press Check again, and contact
          support if it doesn&apos;t.
        </Notice>
      )}

      <button
        type="submit"
        disabled={!stripe || busy}
        data-testid="checkout-pay"
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
            : phase.kind === "settle_timeout"
              ? "Check again"
              : `Pay ${formatCentsAsDollars(totalCents)}`}
      </button>
    </form>
  );
}

/**
 * Stripe returns `payment_method` as either an id or an expanded object,
 * depending on the call. Both shapes appear in practice, so neither is assumed.
 */
function paymentMethodIdOf(
  paymentMethod: string | { id: string } | null | undefined,
): string | null {
  if (!paymentMethod) return null;
  return typeof paymentMethod === "string" ? paymentMethod : paymentMethod.id;
}

/** A failure to CREATE an intent — nothing has been charged. */
function startFailure(err: unknown): string {
  return err instanceof Error
    ? err.message.replace(/^\[[a-z_]+\]\s*/i, "")
    : "We couldn't start this payment. Please try again.";
}

/**
 * A failure to CONFIRM one.
 *
 * card_error and validation_error carry messages written for end users; every
 * other type is an integration or network fault whose message is written for
 * us and must not be shown as though the user did something wrong.
 */
function cardFailure(error: { type?: string; message?: string }): string {
  return (error.type === "card_error" || error.type === "validation_error") &&
    error.message
    ? error.message
    : "We couldn't process that payment. Please try again, or use a different card.";
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
