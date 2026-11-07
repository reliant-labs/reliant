/**
 * The Stripe.js singleton (`../stripe`), against the REAL `@stripe/stripe-js`.
 *
 * The checkout suites mock `../stripe` whole, so this is the one place the
 * wrapper's loading contract is pinned:
 *
 *   - importing it injects no script (the public demo must reach no other
 *     origin; the main `@stripe/stripe-js` entry injects js.stripe.com on
 *     import, which is the regression the `pure` entry fixes);
 *   - a configured getStripe() injects Stripe.js, and once it loads resolves
 *     to the instance built with the runtime publishable key — what
 *     `<Elements stripe={…}>` mounts the payment form on;
 *   - repeated calls share one promise and one script;
 *   - no publishable key (dev) loads nothing and resolves null.
 *
 * jsdom never fetches the script, so the test plays the browser's part: it
 * installs `window.Stripe` and fires the script's `load` event, exactly what
 * the real script does when it arrives.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const config = { STRIPE_PUBLISHABLE_KEY: "" };
vi.mock("@/lib/config_gen", () => ({ loadConfig: () => config }));

type StripeCtor = ((key: string) => unknown) & { version?: number };

function stripeScripts(): HTMLScriptElement[] {
  return Array.from(
    document.querySelectorAll<HTMLScriptElement>(
      'script[src^="https://js.stripe.com"]',
    ),
  );
}

/** A fresh copy of both modules: each holds a module-scope promise. */
async function importFresh() {
  vi.resetModules();
  return import("../stripe");
}

describe("getStripe", () => {
  beforeEach(() => {
    config.STRIPE_PUBLISHABLE_KEY = "";
    for (const s of stripeScripts()) s.remove();
    delete (window as { Stripe?: unknown }).Stripe;
  });

  afterEach(() => {
    for (const s of stripeScripts()) s.remove();
    delete (window as { Stripe?: unknown }).Stripe;
  });

  it("injects no Stripe.js script when the module is imported", async () => {
    config.STRIPE_PUBLISHABLE_KEY = "pk_test_123";
    await importFresh();
    expect(stripeScripts()).toHaveLength(0);
  });

  it("loads Stripe.js on first call and resolves the instance built with the runtime key", async () => {
    config.STRIPE_PUBLISHABLE_KEY = "pk_test_123";
    const { getStripe, isStripeConfigured } = await importFresh();
    expect(isStripeConfigured()).toBe(true);

    const first = getStripe();
    const second = getStripe();
    expect(second).toBe(first);

    const scripts = stripeScripts();
    expect(scripts).toHaveLength(1);
    expect(scripts[0]!.src).toBe("https://js.stripe.com/v3");

    // The browser's part: the script arrives and defines window.Stripe.
    const instance = { elements: vi.fn() };
    const ctor = vi.fn(() => instance) as unknown as StripeCtor;
    ctor.version = 3;
    (window as { Stripe?: unknown }).Stripe = ctor;
    scripts[0]!.dispatchEvent(new Event("load"));

    await expect(first).resolves.toBe(instance);
    expect(ctor).toHaveBeenCalledTimes(1);
    expect(ctor).toHaveBeenCalledWith("pk_test_123");

    // A later checkout reuses the same instance and injects nothing new.
    await expect(getStripe()).resolves.toBe(instance);
    expect(stripeScripts()).toHaveLength(1);
    expect(ctor).toHaveBeenCalledTimes(1);
  });

  it("loads nothing and resolves null without a publishable key", async () => {
    const { getStripe, isStripeConfigured } = await importFresh();
    expect(isStripeConfigured()).toBe(false);
    await expect(getStripe()).resolves.toBeNull();
    expect(stripeScripts()).toHaveLength(0);
  });
});
