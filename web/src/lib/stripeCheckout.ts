/**
 * The Stripe hand-off, in one place.
 *
 * Two defects live here, and both were duplicated across billing.tsx and
 * MobileBillingScreen.tsx — which is how one could be fixed and the other
 * silently not:
 *
 * 1. Every call site passed `window.location.href` as BOTH `successUrl` and
 *    `cancelUrl`, so Stripe returned the user to the identical URL whether
 *    money changed hands or not. The app discarded the one signal Stripe gives
 *    it for free. `buildCheckoutReturnUrls` puts `?checkout=success|cancelled`
 *    on them instead.
 *
 * 2. In the packaged desktop app, setting `window.location.href` to an https
 *    URL is an external navigation relative to `app://bundle`, so Electron
 *    hands it to the system browser. The purchase then completes in a browser
 *    the app has no channel to, and the app window is never notified.
 *    `openCheckout` uses the main-process checkout window when the bridge is
 *    available (see electron/src/stripe-checkout.js), and falls back to the
 *    plain redirect everywhere else.
 *
 * `?checkout=success` is a PRESENTATION signal only — a user can type it, and
 * entitlement stays webhook-driven. What it licenses is showing "confirming
 * your payment…" and refetching, never asserting a subscription the server has
 * not reported.
 */

import { CheckoutUiMode } from "@/gen/controlplane/v1/public/billing_service_pb";

import { getAppURL } from "./constants";

export type CheckoutOutcome = "success" | "cancelled" | "dismissed";

/**
 * The three things a checkout response can mean.
 *
 * `already_complete` is the one that is easy to miss and expensive to get
 * wrong: in dev, plans have no `stripe_price_id`, so the service activates the
 * subscription directly and hands back a response with no session at all. The
 * hosted path absorbed that by accident — `openCheckout` no-ops on an empty or
 * same-origin URL. Embedded mode has no such accident available: mounting an
 * empty client secret renders a provider with nothing in it and hangs on a
 * blank panel. So the case is named here rather than inferred at each caller.
 */
export type CheckoutPresentation =
  | { kind: "embedded"; clientSecret: string; sessionId: string }
  | { kind: "hosted"; checkoutUrl: string; sessionId: string }
  | { kind: "already_complete" };

/** The fields of either create-session response that decide presentation. */
export interface CheckoutSessionResponse {
  uiMode: CheckoutUiMode;
  clientSecret: string;
  checkoutUrl: string;
  sessionId: string;
}

/**
 * Decide how to present a checkout session.
 *
 * Reads `ui_mode`, which the server echoes for exactly this purpose — it does
 * NOT sniff which field came back non-empty. The distinction is not academic:
 * both fields are empty in the dev no-Stripe case, and a sniff classifies that
 * as a hosted session with a missing URL rather than as a completed purchase.
 *
 * Callers get a discriminated union, so adding a fourth mode later is a
 * compile error at each call site rather than a silent fall-through.
 */
export function selectCheckoutPresentation(
  response: CheckoutSessionResponse,
): CheckoutPresentation {
  const { uiMode, clientSecret, checkoutUrl, sessionId } = response;

  if (uiMode === CheckoutUiMode.EMBEDDED) {
    // An embedded response with no secret is the dev path, not a broken embed.
    if (!clientSecret) return { kind: "already_complete" };
    return { kind: "embedded", clientSecret, sessionId };
  }

  // UNSPECIFIED and HOSTED are the same thing — a server that predates
  // embedded checkout leaves the field at zero and means hosted.
  if (!checkoutUrl) return { kind: "already_complete" };
  return { kind: "hosted", checkoutUrl, sessionId };
}

export interface CheckoutReturnUrls {
  successUrl: string;
  cancelUrl: string;
}

/**
 * Build the pair of return URLs for a checkout session.
 *
 * Based on the app's PUBLIC origin rather than `window.location.href`: Stripe
 * redirects the user's browser there, and in the packaged desktop app the
 * document origin is `app://bundle`, which no browser and no Stripe server can
 * resolve (the control-plane redirect allowlist refuses it too). `getAppURL`
 * already encodes that rule for OAuth; checkout needs exactly the same one.
 *
 * @param path App-relative path to return to, e.g. "/settings/billing"
 * @param extra Additional params to preserve across the round trip (plan, from)
 */
export function buildCheckoutReturnUrls(
  path: string,
  extra: Record<string, string | undefined> = {},
): CheckoutReturnUrls {
  const make = (outcome: "success" | "cancelled") => {
    const url = new URL(path, `${getAppURL()}/`);
    for (const [key, value] of Object.entries(extra)) {
      if (value) url.searchParams.set(key, value);
    }
    url.searchParams.set("checkout", outcome);
    return url.toString();
  };
  return { successUrl: make("success"), cancelUrl: make("cancelled") };
}

/**
 * The `/checkout/embed` URL for the Electron checkout window.
 *
 * Built on the app's PUBLIC origin, for a reason that outlived the design's
 * stated one. §2.3 assumed Stripe.js could not load at `app://bundle`; measured
 * against Electron 39 it loads fine. What remains true is that Stripe registers
 * payment-method domains by HOSTNAME, and `app://bundle` can never be one — so
 * a form rendered there would silently lose Apple Pay, Google Pay and Link.
 * Loading our own hosted origin in the existing window keeps those available
 * AND keeps the user inside the app.
 */
export function buildCheckoutEmbedUrl(
  request:
    | { kind: "compute_plan"; planId: string }
    | { kind: "wallet_topup"; amountCents: bigint },
): string {
  const url = new URL("/checkout/embed", `${getAppURL()}/`);
  url.searchParams.set("kind", request.kind);
  if (request.kind === "compute_plan") {
    url.searchParams.set("planId", request.planId);
  } else {
    url.searchParams.set("amountCents", request.amountCents.toString());
  }
  return url.toString();
}

/**
 * Open embedded checkout in the Electron window, if we are in Electron.
 *
 * Returns the outcome when the desktop window hosted the trip, and `null` when
 * there is no bridge — in a browser the caller should mount
 * `EmbeddedCheckoutPanel` in place instead, which is the whole point of
 * embedded mode.
 *
 * Note what is NOT here: `shell.openExternal`, and any `window.location.href`
 * assignment to a stripe.com URL. The system browser is off the checkout path
 * on every surface.
 */
export async function openEmbeddedCheckoutWindow(
  request:
    | { kind: "compute_plan"; planId: string }
    | { kind: "wallet_topup"; amountCents: bigint },
): Promise<CheckoutOutcome | null> {
  const bridge = window.electronAPI?.openStripeCheckout;
  if (!bridge) return null;

  const embedUrl = buildCheckoutEmbedUrl(request);
  // The return URL shares the embed URL's origin, which is what
  // `classifyCheckoutReturn` matches on — the page navigates to
  // `?checkout=success` on itself when the SERVER confirms the purchase.
  const { outcome } = await bridge({
    checkoutUrl: embedUrl,
    returnUrl: embedUrl,
  });
  return outcome;
}

/**
 * Send the user to Stripe and, where possible, learn how it ended.
 *
 * Returns the outcome when the desktop app hosted the round trip. Returns
 * `null` when the browser was navigated away instead — there is no "after" in
 * that case; the answer arrives as `?checkout=` on the next page load.
 *
 * A same-origin URL means dev has no Stripe configured and the action already
 * completed server-side, so there is nothing to open; the caller's invalidated
 * queries refresh in place.
 */
export async function openCheckout(
  url: string,
  returnUrl: string,
): Promise<CheckoutOutcome | null> {
  if (!url) return null;
  if (url.startsWith(window.location.origin)) return null;

  const bridge = window.electronAPI?.openStripeCheckout;
  if (bridge) {
    const { outcome } = await bridge({ checkoutUrl: url, returnUrl });
    return outcome;
  }

  window.location.href = url;
  return null;
}
