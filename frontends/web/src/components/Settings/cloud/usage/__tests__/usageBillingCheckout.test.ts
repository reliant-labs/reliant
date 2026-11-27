import { expect, it } from "vitest";
import { usageBillingReturnUrls } from "../usageBillingCheckout";

// The desktop checkout window settles only on a return carrying `checkout=`
// (electron/src/stripe-checkout.js); `tab=hosting` keeps the billing page from
// reading it as a machine plan purchase.
it("returns to the hosting card with the checkout marker the desktop window settles on", () => {
  const { successUrl, cancelUrl } = usageBillingReturnUrls();
  for (const [value, outcome] of [[successUrl, "success"], [cancelUrl, "cancelled"]] as const) {
    const url = new URL(value);
    expect(url.pathname).toBe("/settings/billing");
    expect(url.searchParams.get("tab")).toBe("hosting");
    expect(url.searchParams.get("checkout")).toBe(outcome);
    expect(url.hash).toBe("#app-hosting");
  }
});
