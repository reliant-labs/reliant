import { buildCheckoutReturnUrls } from "@/lib/stripeCheckout";
import { settingsPath } from "@/lib/paths";

/**
 * Where a hosting checkout returns: the hosting tab's agreement card.
 *
 * The URLs keep the `?checkout=success|cancelled` marker. The desktop app's
 * checkout window settles only on a return that carries it
 * (electron/src/stripe-checkout.js classifyCheckoutReturn); without it the
 * window loads the whole app, signed out, after payment and waits to be closed
 * by hand. `tab=hosting` is what keeps the marker from being read as a
 * machine plan purchase: the billing page shows no plan return banner for it
 * and drops the marker (BillingSection).
 */
export function usageBillingReturnUrls() {
  const { successUrl, cancelUrl } = buildCheckoutReturnUrls(settingsPath("billing"), { tab: "hosting" });
  const anchored = (value: string) => {
    const url = new URL(value);
    url.hash = "app-hosting";
    return url.toString();
  };
  return { successUrl: anchored(successUrl), cancelUrl: anchored(cancelUrl) };
}
