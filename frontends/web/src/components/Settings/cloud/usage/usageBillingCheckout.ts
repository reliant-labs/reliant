import { buildCheckoutReturnUrls } from "@/lib/stripeCheckout";

/** Hosting returns must never be interpreted as a machine checkout success. */
export function usageBillingReturnUrls() {
  const { successUrl, cancelUrl } = buildCheckoutReturnUrls("/settings/billing", { tab: "usage" });
  const clean = (value: string) => {
    const url = new URL(value);
    url.searchParams.delete("checkout");
    url.hash = "app-hosting";
    return url.toString();
  };
  return { successUrl: clean(successUrl), cancelUrl: clean(cancelUrl) };
}
