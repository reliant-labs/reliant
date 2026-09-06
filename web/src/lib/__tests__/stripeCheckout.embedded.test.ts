/**
 * The mode decision, tested in isolation from React.
 *
 * `selectCheckoutPresentation` is the one function that answers "what do I do
 * with this response". Every rule it encodes is one that a call site would
 * otherwise re-derive, and re-derive differently — which is exactly how
 * `redirectToStripe` came to exist twice with one bug fixed in one copy.
 *
 * The rule that gets its own test: the decision reads `ui_mode`, and NOT which
 * field happens to be non-empty. The backend echoes `ui_mode` precisely so the
 * client never has to sniff. A sniffing implementation passes the happy-path
 * cases and then mis-handles the dev no-Stripe response, where BOTH fields are
 * empty — so the sniff test below is written to fail against a sniffing
 * implementation, not just to restate the happy path.
 */

import { describe, expect, it } from "vitest";
import { CheckoutUiMode } from "@/gen/controlplane/v1/public/billing_service_pb";
import { selectCheckoutPresentation } from "../stripeCheckout";

describe("selectCheckoutPresentation", () => {
  it("returns an embedded presentation carrying the client secret", () => {
    const result = selectCheckoutPresentation({
      uiMode: CheckoutUiMode.EMBEDDED,
      clientSecret: "cs_test_secret_123",
      checkoutUrl: "",
      sessionId: "cs_test_123",
    });

    expect(result).toEqual({
      kind: "embedded",
      clientSecret: "cs_test_secret_123",
      sessionId: "cs_test_123",
    });
  });

  it("returns a hosted presentation carrying the URL", () => {
    const result = selectCheckoutPresentation({
      uiMode: CheckoutUiMode.HOSTED,
      clientSecret: "",
      checkoutUrl: "https://checkout.stripe.com/c/pay/cs_test_123",
      sessionId: "cs_test_123",
    });

    expect(result).toEqual({
      kind: "hosted",
      checkoutUrl: "https://checkout.stripe.com/c/pay/cs_test_123",
      sessionId: "cs_test_123",
    });
  });

  it("treats UNSPECIFIED as hosted, so a pre-embedded server still works", () => {
    const result = selectCheckoutPresentation({
      uiMode: CheckoutUiMode.UNSPECIFIED,
      clientSecret: "",
      checkoutUrl: "https://checkout.stripe.com/c/pay/cs_test_123",
      sessionId: "",
    });

    expect(result.kind).toBe("hosted");
  });

  it("reads ui_mode rather than sniffing which field is populated", () => {
    // A server that says EMBEDDED is embedded, even if some future response
    // also carried a URL. An implementation that sniffed `clientSecret &&`
    // would agree here, so the discriminating case is the next test.
    const result = selectCheckoutPresentation({
      uiMode: CheckoutUiMode.EMBEDDED,
      clientSecret: "cs_test_secret_123",
      checkoutUrl: "https://checkout.stripe.com/c/pay/cs_test_123",
      sessionId: "cs_test_123",
    });

    expect(result.kind).toBe("embedded");
  });

  it("reports the dev no-Stripe response as already-complete, not as a broken embed", () => {
    // THE case that a field-sniffing implementation gets wrong. In dev, plans
    // have no stripe_price_id: the service activates the subscription directly
    // and returns a response with neither a client secret nor a foreign URL.
    //
    // A sniff of "clientSecret ? embedded : hosted" classifies this as HOSTED
    // with an empty URL — the old `openCheckout` no-op'd on it by accident. In
    // embedded mode an empty client secret is not a no-op: it would mount a
    // provider with nothing to render and hang on a blank panel. So the
    // already-complete case must be named, not inferred.
    const result = selectCheckoutPresentation({
      uiMode: CheckoutUiMode.UNSPECIFIED,
      clientSecret: "",
      checkoutUrl: "",
      sessionId: "",
    });

    expect(result.kind).toBe("already_complete");
  });

  it("reports an EMBEDDED response with no client secret as already-complete", () => {
    // Same dev path, but against a server that echoes the mode it was asked
    // for. Mounting an empty secret is the failure this prevents.
    const result = selectCheckoutPresentation({
      uiMode: CheckoutUiMode.EMBEDDED,
      clientSecret: "",
      checkoutUrl: "",
      sessionId: "",
    });

    expect(result.kind).toBe("already_complete");
  });
});
