/**
 * `/checkout/embed` — the checkout panel with no app chrome around it.
 *
 * ── Who loads this, and why it exists ─────────────────────────────────
 *
 * The Electron checkout window (`electron/src/stripe-checkout.js`). That window
 * already knows how to watch for a navigation back to the app origin carrying
 * `?checkout=`, and `classifyCheckoutReturn` already decides what such a
 * navigation means. This page reuses that machinery unchanged: it hosts the
 * embedded form, and on a SERVER-CONFIRMED completion navigates to
 * `?checkout=success`, which the window recognises and closes on.
 *
 * ── What the design assumed, and what is actually true ────────────────
 *
 * The design (§2.3) asserts that Stripe.js cannot load at `app://bundle` and
 * builds the whole Electron story on that. Measured against Electron 39 with
 * this app's real scheme registration, that is NOT true: the script loads, an
 * Element's origin-checked postMessage handshake completes, and
 * `initEmbeddedCheckout` reaches Stripe's API (it fails only on a fake client
 * secret). `app://` is registered `standard: true, secure: true`, which is
 * evidently enough for Stripe.js.
 *
 * This page is kept anyway, for the reason that survives that correction:
 * **payment-method domain registration is by HOSTNAME**, and `app://bundle`
 * can never be registered with Stripe. Rendering here means the form runs on
 * our real registered domain, so Apple Pay / Google Pay / Link can appear at
 * all. Loading it in the existing window also keeps the user inside the app —
 * `shell.openExternal` is not on this path.
 *
 * It is deliberately standalone: no sidebar, no modals, no stores it does not
 * need. A 520px window rendering the whole SPA is the cold boot the checkout
 * window exists to avoid.
 */

import { useCallback, useState } from "react";

import { EmbeddedCheckoutPanel } from "./EmbeddedCheckoutPanel";
import type { CheckoutRequest } from "./useCheckoutSession";

/**
 * Read the purchase target off the URL.
 *
 * Returns null for anything unrecognised rather than guessing — this window
 * spends money, and a malformed URL must not resolve to "buy the first thing".
 */
export function parseCheckoutEmbedParams(
  search: string,
): CheckoutRequest | null {
  const params = new URLSearchParams(search);
  const kind = params.get("kind");

  if (kind === "compute_plan") {
    const planId = params.get("planId");
    return planId ? { kind: "compute_plan", planId } : null;
  }

  if (kind === "wallet_topup") {
    const raw = params.get("amountCents");
    if (!raw || !/^\d+$/.test(raw)) return null;
    const amountCents = BigInt(raw);
    return amountCents > 0n ? { kind: "wallet_topup", amountCents } : null;
  }

  return null;
}

export function CheckoutEmbedPage() {
  const [request] = useState(() =>
    parseCheckoutEmbedParams(
      typeof window === "undefined" ? "" : window.location.search,
    ),
  );

  // The signal the Electron window is waiting for. A full-document navigation,
  // not a router transition: the window watches `will-navigate`, and the page
  // is about to be destroyed anyway, so there is nothing to preserve.
  //
  // Reached only from the panel's `onDone`, which fires after the SERVER
  // confirmed the purchase — never straight off Stripe's `onComplete`.
  const handleDone = useCallback(() => {
    const url = new URL(window.location.href);
    url.searchParams.set("checkout", "success");
    window.location.href = url.toString();
  }, []);

  if (!request) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background p-6">
        <p className="text-sm text-muted-foreground">
          This checkout link is missing what to buy. Close this window and try
          again.
        </p>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-background p-4">
      <EmbeddedCheckoutPanel request={request} onDone={handleDone} />
    </div>
  );
}
