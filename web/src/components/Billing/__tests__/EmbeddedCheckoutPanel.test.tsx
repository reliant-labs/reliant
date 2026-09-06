/**
 * The embedded checkout panel.
 *
 * Stripe's own components are mocked — mounting a real `EmbeddedCheckout`
 * would require network access to js.stripe.com and a live session. What is
 * NOT mocked is the thing under test: which client secret we hand the
 * provider, whether we ever navigate, and what `onComplete` is allowed to
 * claim.
 *
 * The mock records the options object it was given, so "mounts the client
 * secret" is asserted against the value that actually reached Stripe rather
 * than against a prop we happened to set.
 */

import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CheckoutUiMode } from "@/gen/controlplane/v1/public/billing_service_pb";

// ── Stripe, mocked at the module boundary ────────────────────────────
//
// `providerOptions` captures what EmbeddedCheckoutProvider received. The
// mock also exposes the captured `onComplete` so a test can fire it the way
// Stripe would, in-page, with no navigation.
const providerOptions: { current: Record<string, unknown> | null } = {
  current: null,
};

vi.mock("@stripe/react-stripe-js", () => ({
  EmbeddedCheckoutProvider: ({
    options,
    children,
  }: {
    options: Record<string, unknown>;
    children: React.ReactNode;
  }) => {
    providerOptions.current = options;
    return <div data-testid="stripe-provider">{children}</div>;
  },
  EmbeddedCheckout: () => <div data-testid="stripe-embedded-checkout" />,
}));

vi.mock("../stripe", () => ({
  getStripe: () => Promise.resolve({}),
  isStripeConfigured: () => true,
}));

// ── The billing mutations ────────────────────────────────────────────
const createCheckout = vi.fn();
const createTopup = vi.fn();
const subscriptionRefetch = vi.fn();
const subscriptionData: { current: unknown } = { current: undefined };

class FakeIdentityRequiredError extends Error {
  constructor() {
    super("identity required");
    this.name = "CheckoutIdentityRequiredError";
  }
}

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useCreateCheckoutSession: () => ({
    mutateAsync: createCheckout,
    isPending: false,
  }),
  useCreateWalletTopupSession: () => ({
    mutateAsync: createTopup,
    isPending: false,
  }),
  // A real hook, not a frozen object. The panel's confirmation gate polls
  // `refetch` and only believes the server's answer, so a mock whose data can
  // never change would make "waits for the server" pass for the wrong reason —
  // it would be waiting forever on a value that could not arrive. This mirrors
  // react-query's contract: refetch re-reads the source and re-renders.
  useComputeSubscription: () => {
    const [, force] = useState(0);
    return {
      data: subscriptionData.current,
      isLoading: false,
      refetch: () => {
        subscriptionRefetch();
        force((n) => n + 1);
        return Promise.resolve({ data: subscriptionData.current });
      },
    };
  },
  useWalletOverview: () => ({
    data: undefined,
    isLoading: false,
    refetch: vi.fn(),
  }),
  isCheckoutIdentityRequired: (e: unknown) =>
    e instanceof FakeIdentityRequiredError,
}));

const { EmbeddedCheckoutPanel } = await import("../EmbeddedCheckoutPanel");

function renderPanel(props: Partial<Record<string, unknown>> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <EmbeddedCheckoutPanel
        request={{ kind: "compute_plan", planId: "plan_small" }}
        onDone={vi.fn()}
        {...props}
      />
    </QueryClientProvider>,
  );
}

const embeddedResponse = {
  uiMode: CheckoutUiMode.EMBEDDED,
  clientSecret: "cs_test_secret_abc",
  checkoutUrl: "",
  sessionId: "cs_test_abc",
};

beforeEach(() => {
  vi.clearAllMocks();
  providerOptions.current = null;
  subscriptionData.current = undefined;
  createCheckout.mockResolvedValue(embeddedResponse);
});

describe("EmbeddedCheckoutPanel", () => {
  it("mounts the client secret the server returned", async () => {
    renderPanel();

    await waitFor(() =>
      expect(screen.getByTestId("stripe-embedded-checkout")).toBeInTheDocument(),
    );
    expect(providerOptions.current?.clientSecret).toBe("cs_test_secret_abc");
  });

  it("never navigates the browser", async () => {
    // The whole point of embedded mode. `openCheckout` set
    // `window.location.href`; if any of that survived, this catches it.
    const assign = vi.fn();
    const originalLocation = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...originalLocation, assign, href: originalLocation.href },
    });

    renderPanel();
    await waitFor(() =>
      expect(screen.getByTestId("stripe-embedded-checkout")).toBeInTheDocument(),
    );

    expect(assign).not.toHaveBeenCalled();
    expect(window.location.href).toBe(originalLocation.href);

    Object.defineProperty(window, "location", {
      configurable: true,
      value: originalLocation,
    });
  });

  it("creates exactly one session across repeated re-renders", async () => {
    // A modal that can be opened and closed makes double-submit easy. The
    // backend sends idempotency keys, but minting on every open still burns
    // sessions and races the reaper. The panel reuses the in-flight one.
    const { rerender } = renderPanel();
    await waitFor(() => expect(createCheckout).toHaveBeenCalledTimes(1));

    const client = new QueryClient();
    for (let i = 0; i < 3; i++) {
      rerender(
        <QueryClientProvider client={client}>
          <EmbeddedCheckoutPanel
            request={{ kind: "compute_plan", planId: "plan_small" }}
            onDone={vi.fn()}
          />
        </QueryClientProvider>,
      );
    }

    expect(createCheckout).toHaveBeenCalledTimes(1);
  });

  it("asks the server for EMBEDDED mode and sends no redirect URLs", async () => {
    renderPanel();
    await waitFor(() => expect(createCheckout).toHaveBeenCalled());

    const args = createCheckout.mock.calls[0][0];
    expect(args.uiMode).toBe(CheckoutUiMode.EMBEDDED);
    // Stripe REJECTS success_url/cancel_url when ui_mode is embedded, and
    // omitting return_url keeps checkout off ALLOWED_REDIRECT_HOSTS entirely.
    expect(args.successUrl ?? "").toBe("");
    expect(args.cancelUrl ?? "").toBe("");
    expect(args.returnUrl ?? "").toBe("");
  });

  describe("onComplete", () => {
    it("does NOT claim the plan is active on its own", async () => {
      // The invariant. `onComplete` is a presentation signal exactly like
      // `?checkout=success` was — entitlement is webhook-driven, and the
      // server has reported nothing yet here.
      //
      // `onDone` is the assertion that carries the weight. The visible-text
      // checks below pass even against a panel that trusts `onComplete`
      // outright (with no plan in hand it has no name to render), so a test
      // that stopped there would be green against exactly the bug it exists to
      // prevent — verified by mutation. Calling `onDone` is what tells the
      // CALLER the purchase is real, so it is the thing that must not happen
      // until the server agrees.
      const onDone = vi.fn();
      renderPanel({ onDone });
      await waitFor(() => expect(providerOptions.current).not.toBeNull());

      const onComplete = providerOptions.current?.onComplete as () => void;
      expect(typeof onComplete).toBe("function");
      onComplete();

      await waitFor(() =>
        expect(screen.getByText(/confirming your payment/i)).toBeInTheDocument(),
      );
      expect(onDone).not.toHaveBeenCalled();
      // And nothing on screen may assert the purchase succeeded.
      expect(screen.queryByText(/you're on/i)).not.toBeInTheDocument();
      expect(screen.queryByText(/subscription active/i)).not.toBeInTheDocument();
    });

    it("only names the plan once the SERVER reports it", async () => {
      renderPanel();
      await waitFor(() => expect(providerOptions.current).not.toBeNull());
      (providerOptions.current?.onComplete as () => void)();

      await waitFor(() =>
        expect(screen.getByText(/confirming your payment/i)).toBeInTheDocument(),
      );

      // The webhook lands and the subscription query now reports the plan.
      subscriptionData.current = {
        subscription: { plan: { id: "plan_small", name: "Small" } },
      };

      await waitFor(
        () => expect(screen.getByText(/small/i)).toBeInTheDocument(),
        { timeout: 5000 },
      );
    });

    it("polls the subscription while confirmation is in flight", async () => {
      renderPanel();
      await waitFor(() => expect(providerOptions.current).not.toBeNull());
      (providerOptions.current?.onComplete as () => void)();

      await waitFor(() => expect(subscriptionRefetch).toHaveBeenCalled(), {
        timeout: 5000,
      });
    });
  });

  it("surfaces the anonymous-purchase refusal instead of a payment form", async () => {
    // The guarantee lives in the mutation, and it must fire BEFORE a session
    // exists. If the panel rendered a form here, an anonymous user would be
    // looking at a card field for a purchase that can never be delivered.
    createCheckout.mockRejectedValue(new FakeIdentityRequiredError());

    // Asserted through `renderIdentityRequired`, which ONLY the identity state
    // invokes. Asserting on the message text instead would pass against a
    // panel that treated this as a generic error — the message is the same
    // either way, so the text check cannot tell the two apart, and the caller
    // would render "something went wrong" instead of the affordance that lets
    // the user actually link an email. Verified by mutation.
    const renderIdentityRequired = vi.fn((message: string) => (
      <div data-testid="identity-affordance">{message}</div>
    ));

    renderPanel({ renderIdentityRequired });

    await waitFor(() =>
      expect(screen.getByTestId("identity-affordance")).toBeInTheDocument(),
    );
    expect(renderIdentityRequired).toHaveBeenCalledWith("identity required");
    // And no payment form was ever built for a purchase that cannot be
    // delivered — the refusal fired inside the mutation, before any session.
    expect(
      screen.queryByTestId("stripe-embedded-checkout"),
    ).not.toBeInTheDocument();
    expect(providerOptions.current).toBeNull();
  });

  it("reports the dev no-Stripe response as done rather than mounting an empty form", async () => {
    createCheckout.mockResolvedValue({
      uiMode: CheckoutUiMode.UNSPECIFIED,
      clientSecret: "",
      checkoutUrl: "",
      sessionId: "",
    });
    const onDone = vi.fn();

    renderPanel({ onDone });

    await waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(
      screen.queryByTestId("stripe-embedded-checkout"),
    ).not.toBeInTheDocument();
  });

  it("creates a wallet top-up session for the wallet_topup kind", async () => {
    createTopup.mockResolvedValue({
      ...embeddedResponse,
      clientSecret: "cs_test_topup_secret",
    });

    renderPanel({ request: { kind: "wallet_topup", amountCents: 2000n } });

    await waitFor(() => expect(createTopup).toHaveBeenCalled());
    expect(createCheckout).not.toHaveBeenCalled();
    expect(createTopup.mock.calls[0][0].amountCents).toBe(2000n);
    expect(createTopup.mock.calls[0][0].uiMode).toBe(CheckoutUiMode.EMBEDDED);
  });
});
