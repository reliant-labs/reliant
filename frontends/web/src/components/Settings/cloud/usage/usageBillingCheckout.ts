import { buildCheckoutReturnUrls } from "@/lib/stripeCheckout";
import { settingsPath } from "@/lib/paths";

/** Hosting returns must never be interpreted as a machine checkout success. */
export function usageBillingReturnUrls() {
  const { successUrl, cancelUrl } = buildCheckoutReturnUrls(settingsPath("billing"), { tab: "usage" });
  const clean = (value: string) => {
    const url = new URL(value);
    url.searchParams.delete("checkout");
    url.hash = "app-hosting";
    return url.toString();
  };
  return { successUrl: clean(successUrl), cancelUrl: clean(cancelUrl) };
}
