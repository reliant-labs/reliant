const test = require("node:test");
const assert = require("node:assert");
const { EventEmitter } = require("node:events");
const {
  classifyCheckoutReturn,
  openStripeCheckout,
} = require("../src/stripe-checkout");

const RETURN = "https://app.reliantlabs.io/settings/billing?checkout=success";

test("a return to the app origin with checkout=success settles as success", () => {
  assert.strictEqual(
    classifyCheckoutReturn(
      "https://app.reliantlabs.io/settings/billing?checkout=success&plan=compute-standard",
      RETURN,
    ),
    "success",
  );
});

test("a return with checkout=cancelled settles as cancelled", () => {
  assert.strictEqual(
    classifyCheckoutReturn(
      "https://app.reliantlabs.io/settings/billing?checkout=cancelled",
      RETURN,
    ),
    "cancelled",
  );
});

test("Stripe's own internal navigations are not a return", () => {
  // Hosted checkout navigates itself repeatedly (3DS, payment method steps).
  // Settling on any of them would close the window mid-purchase.
  for (const url of [
    "https://checkout.stripe.com/c/pay/cs_test_123",
    "https://checkout.stripe.com/c/pay/cs_test_123#step=payment",
    "https://hooks.stripe.com/3d_secure/authenticate",
  ]) {
    assert.strictEqual(classifyCheckoutReturn(url, RETURN), null, url);
  }
});

test("a checkout marker on some OTHER origin is ignored", () => {
  // Otherwise a page Stripe redirects through could fake a completed purchase.
  assert.strictEqual(
    classifyCheckoutReturn(
      "https://evil.example.test/settings/billing?checkout=success",
      RETURN,
    ),
    null,
  );
});

test("an unrecognised checkout value keeps the flow waiting", () => {
  assert.strictEqual(
    classifyCheckoutReturn(
      "https://app.reliantlabs.io/settings/billing?checkout=maybe",
      RETURN,
    ),
    null,
  );
  assert.strictEqual(
    classifyCheckoutReturn("https://app.reliantlabs.io/settings/billing", RETURN),
    null,
  );
});

test("unparseable URLs are not a return", () => {
  assert.strictEqual(classifyCheckoutReturn("not a url", RETURN), null);
  assert.strictEqual(classifyCheckoutReturn(RETURN, "not a url"), null);
});

// ── The window plumbing, with a fake BrowserWindow ──────────────────────

function fakeBrowserWindowClass(record) {
  return class FakeBrowserWindow extends EventEmitter {
    constructor(options) {
      super();
      record.options = options;
      record.instance = this;
      this.webContents = new EventEmitter();
      this.destroyed = false;
    }
    loadURL(url) {
      record.loadedUrl = url;
      return Promise.resolve();
    }
    isDestroyed() {
      return this.destroyed;
    }
    destroy() {
      this.destroyed = true;
    }
  };
}

test("resolves success when the checkout window navigates back", async () => {
  const record = {};
  const promise = openStripeCheckout({
    BrowserWindow: fakeBrowserWindowClass(record),
    checkoutUrl: "https://checkout.stripe.com/c/pay/cs_test_123",
    returnUrl: RETURN,
  });

  assert.strictEqual(record.loadedUrl, "https://checkout.stripe.com/c/pay/cs_test_123");
  // Third-party content gets its own partition and no node access.
  assert.strictEqual(record.options.webPreferences.nodeIntegration, false);
  assert.strictEqual(record.options.webPreferences.partition, "stripe-checkout");

  const event = { defaultPrevented: false, preventDefault() { this.defaultPrevented = true; } };
  record.instance.webContents.emit(
    "will-navigate",
    event,
    "https://app.reliantlabs.io/settings/billing?checkout=success",
  );

  assert.deepStrictEqual(await promise, { outcome: "success" });
  // The return page is a signal, not something to render in a popup.
  assert.strictEqual(event.defaultPrevented, true);
  assert.strictEqual(record.instance.isDestroyed(), true);
});

test("a will-redirect back also settles the flow", async () => {
  const record = {};
  const promise = openStripeCheckout({
    BrowserWindow: fakeBrowserWindowClass(record),
    checkoutUrl: "https://checkout.stripe.com/c/pay/cs_test_123",
    returnUrl: RETURN,
  });

  record.instance.webContents.emit(
    "will-redirect",
    { preventDefault() {} },
    "https://app.reliantlabs.io/settings/billing?checkout=cancelled",
  );

  assert.deepStrictEqual(await promise, { outcome: "cancelled" });
});

test("closing the window is a dismissal, distinct from a cancellation", async () => {
  const record = {};
  const promise = openStripeCheckout({
    BrowserWindow: fakeBrowserWindowClass(record),
    checkoutUrl: "https://checkout.stripe.com/c/pay/cs_test_123",
    returnUrl: RETURN,
  });

  record.instance.emit("closed");

  assert.deepStrictEqual(await promise, { outcome: "dismissed" });
});

// ── Embedded checkout: /checkout/embed in the same window ───────────────
//
// The renderer now opens OUR page rather than checkout.stripe.com. The window
// machinery is unchanged — which is the point — so what these pin is that the
// embedded URL still goes through the BrowserWindow, and that its
// same-origin return is recognised.

const EMBED_URL =
  "https://app.reliantlabs.io/checkout/embed?kind=compute_plan&planId=plan_small";

test("the embedded checkout page loads in the window, not the system browser", async () => {
  const record = {};
  const promise = openStripeCheckout({
    BrowserWindow: fakeBrowserWindowClass(record),
    checkoutUrl: EMBED_URL,
    returnUrl: EMBED_URL,
  });

  // A BrowserWindow was constructed and given the URL. Nothing here can reach
  // shell.openExternal: openStripeCheckout is not passed `shell` at all, so
  // the system-browser hand-off is unavailable by construction rather than by
  // convention.
  assert.strictEqual(record.loadedUrl, EMBED_URL);
  assert.ok(record.instance, "a checkout window was created");

  record.instance.emit("closed");
  await promise;
});

test("the embed page's own ?checkout=success navigation settles the flow", async () => {
  // The embed page navigates to itself with the marker appended once the
  // SERVER confirms the purchase. Same origin as returnUrl, so the existing
  // classifier recognises it with no change.
  const record = {};
  const promise = openStripeCheckout({
    BrowserWindow: fakeBrowserWindowClass(record),
    checkoutUrl: EMBED_URL,
    returnUrl: EMBED_URL,
  });

  record.instance.webContents.emit(
    "will-navigate",
    { preventDefault() {} },
    `${EMBED_URL}&checkout=success`,
  );

  assert.deepStrictEqual(await promise, { outcome: "success" });
});

test("Stripe's iframe origins are not mistaken for the embed page returning", () => {
  // Embedded checkout runs js.stripe.com in an iframe inside our page. Those
  // are different origins from the app's, so they must never settle the
  // window — a settle mid-payment closes the form while the user is typing.
  for (const url of [
    "https://js.stripe.com/v3/",
    "https://js.stripe.com/v3/fingerprinted/js/controller.js",
    "https://hooks.stripe.com/3d_secure_2/authenticate",
  ]) {
    assert.strictEqual(classifyCheckoutReturn(url, EMBED_URL), null, url);
  }
});
