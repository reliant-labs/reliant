/**
 * Stripe checkout as a ROUND TRIP the app can observe, rather than a one-way
 * exit into the system browser.
 *
 * ── What was wrong ────────────────────────────────────────────────────
 *
 * The renderer set `window.location.href` to `https://checkout.stripe.com/…`.
 * `shouldOpenExternally` correctly classified that as external — an https
 * target against an `app://bundle` origin IS external, and the same is true in
 * dev from `localhost:3000` — so main.js handed it to `shell.openExternal`.
 * The purchase then completed in a browser tab the app has no channel to,
 * Stripe's `successUrl` landed in that browser (possibly in a signed-out
 * session), and the app window behind it was never told anything happened.
 *
 * ── Why the navigation policy is NOT the fix ──────────────────────────
 *
 * It is tempting to make `shouldOpenExternally` return false for Stripe. That
 * would encode a wrong rule: the classification is right, and a test asserting
 * `false` for a Stripe URL would pin the wrong behaviour in place. The defect
 * is not misclassification — it is that a round trip had no return path. So the
 * policy is untouched and the trip gets a window that watches for the return.
 *
 * ── How the return is detected ────────────────────────────────────────
 *
 * Stripe is given a `successUrl` / `cancelUrl` on the HOSTED app origin (an
 * `app://` URL is unreachable from Stripe's servers and is refused by the
 * control-plane redirect allowlist anyway). We never let that page load: the
 * checkout window's own navigation events are watched, and the first navigation
 * back to that origin carrying `?checkout=` settles the flow and closes the
 * window. The renderer then moves in place — no cold boot, no browser tab.
 */

const log = require("electron-log");

/**
 * Classify a navigation out of the checkout window.
 *
 * Pure, and separate from the window plumbing, because this is the part with
 * rules worth pinning: it must not settle on Stripe's own internal navigations
 * (which are numerous), must not honour a return to some other origin, and must
 * treat an unrecognised `checkout` value as "keep waiting" rather than guessing.
 *
 * @param {string} targetUrl URL the checkout window is navigating to
 * @param {string} returnOrigin Origin of the app's own return URL
 * @returns {"success"|"cancelled"|null} null means "not a return; keep waiting"
 */
function classifyCheckoutReturn(targetUrl, returnOrigin) {
  let target;
  let expected;
  try {
    target = new URL(targetUrl);
    expected = new URL(returnOrigin);
  } catch {
    return null;
  }

  if (target.origin !== expected.origin) return null;

  const outcome = target.searchParams.get("checkout");
  if (outcome === "success" || outcome === "cancelled") return outcome;
  return null;
}

/**
 * Open Stripe checkout in a controlled window and resolve with the outcome.
 *
 * Resolves `{ outcome: "success" | "cancelled" }` when the window navigates
 * back to `returnUrl`'s origin with a `checkout` marker, or
 * `{ outcome: "dismissed" }` if the user simply closes the window — which is a
 * real thing users do and is NOT the same as Stripe reporting a cancellation.
 *
 * @param {object} deps
 * @param {typeof import("electron").BrowserWindow} deps.BrowserWindow
 * @param {import("electron").BrowserWindow} [deps.parent]
 * @param {string} deps.checkoutUrl Hosted Stripe URL from the backend
 * @param {string} deps.returnUrl successUrl handed to Stripe (origin is matched)
 * @returns {Promise<{outcome: "success"|"cancelled"|"dismissed"}>}
 */
function openStripeCheckout({ BrowserWindow, parent, checkoutUrl, returnUrl }) {
  return new Promise((resolve, reject) => {
    let checkoutWindow;
    try {
      checkoutWindow = new BrowserWindow({
        width: 520,
        height: 780,
        parent: parent && !parent.isDestroyed() ? parent : undefined,
        modal: false,
        show: true,
        title: "Secure checkout",
        autoHideMenuBar: true,
        webPreferences: {
          // Stripe's page is third-party content: no preload, no node, and its
          // own session partition so it cannot see the app's cookies.
          nodeIntegration: false,
          contextIsolation: true,
          sandbox: true,
          partition: "stripe-checkout",
        },
      });
    } catch (error) {
      reject(error);
      return;
    }

    let settled = false;
    const settle = (outcome) => {
      if (settled) return;
      settled = true;
      resolve({ outcome });
      if (checkoutWindow && !checkoutWindow.isDestroyed()) {
        checkoutWindow.destroy();
      }
    };

    const onNavigation = (event, url) => {
      const outcome = classifyCheckoutReturn(url, returnUrl);
      if (!outcome) return;
      // Do not let the return page load — it exists only as a signal, and
      // rendering the whole SPA inside a 520px popup is exactly the cold boot
      // this is meant to avoid.
      if (event && typeof event.preventDefault === "function") {
        event.preventDefault();
      }
      log.info("[StripeCheckout] Checkout returned", { outcome });
      settle(outcome);
    };

    checkoutWindow.webContents.on("will-navigate", onNavigation);
    checkoutWindow.webContents.on("will-redirect", onNavigation);

    // Closing the window is a dismissal, not a Stripe-reported cancellation.
    // The caller refetches either way, so a purchase completed in the instant
    // before a close is still picked up.
    checkoutWindow.on("closed", () => {
      if (settled) return;
      settled = true;
      log.info("[StripeCheckout] Checkout window closed by the user");
      resolve({ outcome: "dismissed" });
    });

    checkoutWindow.loadURL(checkoutUrl).catch((error) => {
      log.error("[StripeCheckout] Failed to load checkout:", error);
      if (settled) return;
      settled = true;
      reject(error);
      if (checkoutWindow && !checkoutWindow.isDestroyed()) {
        checkoutWindow.destroy();
      }
    });
  });
}

module.exports = {
  classifyCheckoutReturn,
  openStripeCheckout,
};
