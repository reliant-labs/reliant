/**
 * Stripe's payment form, mounted in-page. The only thing in the app that does.
 *
 * ── The rule that must not be relaxed ─────────────────────────────────
 *
 * `onComplete` fires when Stripe's iframe reports the payment finished. It is
 * a PRESENTATION signal and nothing more — exactly the status
 * `?checkout=success` had, and for the same reason: entitlement is decided by
 * Stripe's webhook reaching our server, not by anything the browser observed.
 * The two can be seconds apart, and the second one can fail.
 *
 * So `onComplete` licenses showing "Confirming your payment…" and polling. It
 * NEVER names the plan, and it never tells the caller the purchase succeeded.
 * That only happens once `useComputeSubscription` reports the plan back. The
 * poll is the same shape `CheckoutReturnBanner` established — 2s interval,
 * 60s cap — because an unbounded poll against a payment that genuinely failed
 * spins forever.
 *
 * ── Why there is no row of payment-method logos ───────────────────────
 *
 * Deliberate. Which methods are available depends on the browser (Apple Pay is
 * largely Safari), on payment-method domain registration having been done for
 * that exact hostname, and on Dashboard configuration. A static row promises
 * methods we then fail to display, which is worse than showing none. Stripe's
 * own UI enumerates what is actually offered.
 */

import { useEffect, useState } from "react";
import { EmbeddedCheckout, EmbeddedCheckoutProvider } from "@stripe/react-stripe-js";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";

import { useComputeSubscription } from "@/hooks/useCloudBillingQueries";

import { getStripe } from "./stripe";
import { useCheckoutSession, type CheckoutRequest } from "./useCheckoutSession";

export type { CheckoutRequest } from "./useCheckoutSession";

export interface EmbeddedCheckoutPanelProps {
  /** What to buy. Changing this starts a new session; re-renders do not. */
  request: CheckoutRequest;
  /**
   * Called when the panel has nothing left to do — the payment was confirmed
   * by the SERVER, the dev no-Stripe path completed the purchase outright, or
   * the user dismissed. Never called merely because `onComplete` fired.
   */
  onDone: () => void;
  /** Rendered when the user must link an identity before purchasing. */
  renderIdentityRequired?: (message: string) => React.ReactNode;
  className?: string;
}

/** How long to wait for the webhook before saying we could not confirm. */
const CONFIRM_TIMEOUT_MS = 60_000;
const CONFIRM_POLL_MS = 2_000;

export function EmbeddedCheckoutPanel({
  request,
  onDone,
  renderIdentityRequired,
  className,
}: EmbeddedCheckoutPanelProps) {
  const session = useCheckoutSession(request);
  const [paymentReported, setPaymentReported] = useState(false);

  // The dev no-Stripe path: the server already did the work, so there is no
  // form to render. Reported through the same `onDone` as a real completion so
  // callers need no dev-specific branch.
  const alreadyComplete =
    session.status === "ready" &&
    session.presentation.kind === "already_complete";
  useEffect(() => {
    if (alreadyComplete) onDone();
  }, [alreadyComplete, onDone]);

  if (session.status === "identity_required") {
    return (
      <div className={className}>
        {renderIdentityRequired ? (
          renderIdentityRequired(session.message)
        ) : (
          <div className="flex items-start gap-2 rounded-md border border-border bg-muted/40 px-4 py-3">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
            <p className="text-sm text-foreground">{session.message}</p>
          </div>
        )}
      </div>
    );
  }

  if (session.status === "error") {
    return (
      <div className={className}>
        <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-3">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
          <p className="text-sm text-destructive">{session.message}</p>
        </div>
      </div>
    );
  }

  // `alreadyComplete` is checked again here rather than reused as a boolean so
  // the union narrows: after this block the presentation is embedded-or-hosted.
  if (
    session.status === "creating" ||
    session.presentation.kind === "already_complete"
  ) {
    return (
      <div className={className}>
        <div className="flex items-center gap-2 px-4 py-6 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Preparing secure checkout…
        </div>
      </div>
    );
  }

  if (session.presentation.kind === "hosted") {
    // The server chose hosted mode. The panel does not silently redirect —
    // the caller asked for an embedded form and should decide what to do
    // instead, rather than having the browser navigated out from under it.
    return (
      <div className={className}>
        <div className="flex items-start gap-2 rounded-md border border-border bg-muted/40 px-4 py-3">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
          <p className="text-sm text-foreground">
            This checkout needs to open in a separate window.
          </p>
        </div>
      </div>
    );
  }

  if (paymentReported) {
    return (
      <div className={className}>
        <PaymentConfirmation request={request} onConfirmed={onDone} />
      </div>
    );
  }

  return (
    <div className={className}>
      <EmbeddedCheckoutProvider
        stripe={getStripe()}
        options={{
          clientSecret: session.presentation.clientSecret,
          // Stripe reports completion in-page: the session is created with
          // redirect_on_completion "never", so nothing navigates.
          onComplete: () => setPaymentReported(true),
        }}
      >
        <EmbeddedCheckout />
      </EmbeddedCheckoutProvider>
    </div>
  );
}

/**
 * The gap between "Stripe says paid" and "our server agrees".
 *
 * Renders a claim about the purchase ONLY from `useComputeSubscription` — the
 * server's answer. Until that lands this says it is confirming, and if it
 * never lands it says so honestly rather than quietly implying success.
 */
function PaymentConfirmation({
  request,
  onConfirmed,
}: {
  request: CheckoutRequest;
  onConfirmed: () => void;
}) {
  const subQ = useComputeSubscription();
  const [timedOut, setTimedOut] = useState(false);

  const plan = subQ.data?.subscription?.plan ?? null;
  const confirmed =
    request.kind === "compute_plan"
      ? !!plan && plan.id === request.planId
      : // A top-up has no subscription to confirm against; the wallet query the
        // caller already owns is what reflects it.
        true;

  // Depends on `refetch`, which react-query keeps stable — NOT on the query
  // result, which is a fresh object every render and would tear the interval
  // down and rebuild it, resetting the 60s cap forever.
  const refetch = subQ.refetch;
  useEffect(() => {
    if (confirmed) return;
    const poll = setInterval(() => void refetch(), CONFIRM_POLL_MS);
    const stop = setTimeout(() => {
      clearInterval(poll);
      setTimedOut(true);
    }, CONFIRM_TIMEOUT_MS);
    return () => {
      clearInterval(poll);
      clearTimeout(stop);
    };
  }, [confirmed, refetch]);

  useEffect(() => {
    if (confirmed) onConfirmed();
  }, [confirmed, onConfirmed]);

  if (confirmed && request.kind === "compute_plan" && plan) {
    return (
      <div className="flex items-start gap-2 rounded-md border border-primary/40 bg-primary/10 px-4 py-3">
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
        <p className="text-sm text-foreground">
          You&apos;re on <span className="font-medium">{plan.name}</span>.
        </p>
      </div>
    );
  }

  if (timedOut) {
    return (
      <div className="flex items-start gap-2 rounded-md border border-border bg-muted/40 px-4 py-3">
        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
        <p className="text-sm text-foreground">
          Your payment went through, but we haven&apos;t been able to confirm it
          yet. It usually lands within a minute — refresh to check, and contact
          support if it doesn&apos;t.
        </p>
      </div>
    );
  }

  return (
    <div className="flex items-center gap-2 rounded-md border border-primary/40 bg-primary/10 px-4 py-3">
      <Loader2 className="h-4 w-4 shrink-0 animate-spin text-primary" />
      <p className="text-sm text-foreground">
        Confirming your payment… this usually takes a few seconds.
      </p>
    </div>
  );
}
