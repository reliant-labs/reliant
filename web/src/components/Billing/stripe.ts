/**
 * The Stripe.js singleton.
 *
 * `loadStripe` injects a <script> and memoises the promise internally, but the
 * memo is per-module-instance; calling it from several components still risks
 * several publishable-key reads and several `Stripe()` constructions. One
 * module-scope promise, created lazily, removes the question.
 *
 * ── The version pin is load-bearing, and it is not cosmetic ───────────
 *
 * `@stripe/stripe-js` selects a Stripe.js RELEASE TRAIN by major version, and
 * the trains disagree about embedded checkout:
 *
 *   - v5 (`^5.10.0`) loads `https://js.stripe.com/v3`, whose API is
 *     `stripe.initEmbeddedCheckout({ clientSecret })` and which accepts
 *     sessions created with `ui_mode=embedded`.
 *   - v9 (latest) loads the `dahlia` train, whose API is
 *     `stripe.createEmbeddedCheckoutPage()` and which REJECTS our sessions
 *     with: "Only Checkout sessions with ui_mode=embedded_page can be used
 *     with embedded Checkout."
 *
 * The backend builds sessions with `stripe-go/v82`, whose only embedded
 * constant is `CheckoutSessionUIModeEmbedded = "embedded"` — there is no
 * `embedded_page` to send. So v9 cannot work against this server at all, and
 * the failure appears at RUNTIME, in the payment form, with a green build.
 *
 * Both package.json entries are therefore pinned EXACTLY, not with a caret. A
 * caret would let a `npm update` cross the train boundary and break checkout
 * with no source change to review. Verified empirically against both trains;
 * if the backend ever moves to a stripe-go with `embedded_page`, these two
 * pins move together with it.
 */

import { loadStripe, type Stripe } from "@stripe/stripe-js";

let stripePromise: Promise<Stripe | null> | null = null;

/**
 * The publishable key. Absent in dev, where plans carry no `stripe_price_id`
 * and the server completes purchases without Stripe at all — so a missing key
 * is a normal state, not a misconfiguration to throw on.
 */
function publishableKey(): string {
  const key = import.meta.env.VITE_STRIPE_PUBLISHABLE_KEY;
  return typeof key === "string" ? key : "";
}

export function isStripeConfigured(): boolean {
  return publishableKey().length > 0;
}

export function getStripe(): Promise<Stripe | null> {
  if (!isStripeConfigured()) return Promise.resolve(null);
  if (!stripePromise) stripePromise = loadStripe(publishableKey());
  return stripePromise;
}
