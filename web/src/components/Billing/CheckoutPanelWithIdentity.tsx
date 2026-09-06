/**
 * `EmbeddedCheckoutPanel` plus the in-place answer to its one refusal.
 *
 * The panel reports `identity_required` and renders whatever the caller hands
 * it. Every surface that can hit that state wants the same response — ask for
 * an identity, in place, then carry on — so it lives here once rather than
 * three times.
 *
 * ── Why the retry needs a key ─────────────────────────────────────────
 *
 * `useCheckoutSession` starts exactly one session per purchase target and
 * remembers that it did, which is what stops a modal minting a session per
 * open. That memory also means clearing the refusal is not enough to make it
 * try again: the target has not changed. So a successful link bumps
 * `linkAttempt`, which is part of the panel's key, and the panel remounts into
 * a fresh session — now with a real identity behind it, so the mutation lets
 * it through.
 *
 * Nothing here relaxes the rule itself. `assertPurchaseIdentity` still runs
 * inside the mutation on the retry exactly as it did on the first attempt; if
 * the link did not actually take, the panel lands right back in this state.
 */

import { useState } from "react";

import {
  EmbeddedCheckoutPanel,
  type CheckoutRequest,
} from "./EmbeddedCheckoutPanel";
import { LinkIdentityModal } from "./LinkIdentityModal";

export interface CheckoutPanelWithIdentityProps {
  request: CheckoutRequest;
  onDone: () => void;
  /**
   * Where an OAuth link should land the user. Only the provider buttons use
   * it — the email path completes in the modal and never leaves the page.
   */
  returnTo?: string;
  className?: string;
}

/** Stable identity for a purchase target, so changing it starts a new session. */
function requestKey(request: CheckoutRequest): string {
  return request.kind === "compute_plan"
    ? `compute_plan:${request.planId}`
    : `wallet_topup:${request.amountCents}`;
}

export function CheckoutPanelWithIdentity({
  request,
  onDone,
  returnTo,
  className,
}: CheckoutPanelWithIdentityProps) {
  const [linkAttempt, setLinkAttempt] = useState(0);
  // The user closed the modal without linking. The panel stays refused — it
  // must, the purchase genuinely cannot proceed — but we stop shoving a dialog
  // in front of someone who has said no.
  const [dismissed, setDismissed] = useState(false);

  return (
    <EmbeddedCheckoutPanel
      key={`${requestKey(request)}:${linkAttempt}`}
      request={request}
      onDone={onDone}
      className={className}
      renderIdentityRequired={(message) =>
        dismissed ? (
          <IdentityDismissedNotice
            message={message}
            onReopen={() => setDismissed(false)}
          />
        ) : (
          <LinkIdentityModal
            message={message}
            returnTo={returnTo}
            onLinked={() => setLinkAttempt((n) => n + 1)}
            onDismiss={() => setDismissed(true)}
          />
        )
      }
    />
  );
}

/**
 * What is left behind when the user closes the modal. It has to keep saying
 * why checkout is stuck — silently rendering nothing would look like a broken
 * payment form — and it has to offer the way back in.
 */
function IdentityDismissedNotice({
  message,
  onReopen,
}: {
  message: string;
  onReopen: () => void;
}) {
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-muted/40 px-4 py-3">
      <p className="text-sm text-foreground">{message}</p>
      <div>
        <button
          type="button"
          onClick={onReopen}
          className="rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus:outline-none focus:ring-2 focus:ring-primary"
        >
          Add an email to this account
        </button>
      </div>
    </div>
  );
}
