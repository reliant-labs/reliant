/**
 * The wrapper that turns the panel's identity refusal into an in-place ask.
 *
 * The load-bearing assertion is the RETRY: `useCheckoutSession` remembers that
 * it already started a session for a purchase target, so clearing the refusal
 * is not enough to make it try again. If the remount key were wrong the modal
 * would close onto a panel still showing the refusal — which looks like the
 * link silently failed. That is asserted by counting calls to the checkout
 * mutation, not by looking at what is on screen.
 *
 * `LinkIdentityModal` is stubbed here: it has its own tests, and stubbing it
 * lets this file drive "the user linked successfully" without standing up
 * Supabase. What is NOT stubbed is the panel or the session hook — the things
 * whose interaction is under test.
 */

import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CheckoutUiMode } from "@/gen/controlplane/v1/public/billing_service_pb";

vi.mock("@stripe/react-stripe-js", () => ({
  EmbeddedCheckoutProvider: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="stripe-provider">{children}</div>
  ),
  EmbeddedCheckout: () => <div data-testid="stripe-embedded-checkout" />,
}));

vi.mock("../stripe", () => ({
  getStripe: () => Promise.resolve({}),
  isStripeConfigured: () => true,
}));

// The modal, reduced to the two things the wrapper reacts to.
const modalProps: { current: Record<string, unknown> | null } = { current: null };
vi.mock("../LinkIdentityModal", () => ({
  LinkIdentityModal: (props: Record<string, unknown>) => {
    modalProps.current = props;
    return (
      <div data-testid="link-identity-modal">
        <button onClick={props.onLinked as () => void}>stub-link</button>
        <button onClick={props.onDismiss as () => void}>stub-dismiss</button>
      </div>
    );
  },
}));

const createCheckout = vi.fn();

class FakeIdentityRequiredError extends Error {
  constructor() {
    super("identity required");
    this.name = "CheckoutIdentityRequiredError";
  }
}

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useCreateCheckoutSession: () => ({ mutateAsync: createCheckout, isPending: false }),
  useCreateWalletTopupSession: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useComputeSubscription: () => {
    const [, force] = useState(0);
    return { data: undefined, isLoading: false, refetch: () => { force((n) => n + 1); return Promise.resolve({}); } };
  },
  useWalletOverview: () => ({ data: undefined, isLoading: false, refetch: vi.fn() }),
  isCheckoutIdentityRequired: (e: unknown) => e instanceof FakeIdentityRequiredError,
}));

const { CheckoutPanelWithIdentity } = await import("../CheckoutPanelWithIdentity");

const embeddedResponse = {
  uiMode: CheckoutUiMode.EMBEDDED,
  clientSecret: "cs_test_secret_abc",
  checkoutUrl: "",
  sessionId: "cs_test_abc",
};

function renderWrapper(props: Record<string, unknown> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <CheckoutPanelWithIdentity
        request={{ kind: "compute_plan", planId: "plan_small" }}
        onDone={vi.fn()}
        {...props}
      />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  modalProps.current = null;
});

describe("CheckoutPanelWithIdentity", () => {
  it("opens the modal instead of a payment form when the purchase is refused", async () => {
    createCheckout.mockRejectedValue(new FakeIdentityRequiredError());

    renderWrapper();

    await waitFor(() =>
      expect(screen.getByTestId("link-identity-modal")).toBeInTheDocument(),
    );
    // No card field is ever built for a purchase that cannot be delivered.
    expect(screen.queryByTestId("stripe-embedded-checkout")).not.toBeInTheDocument();
    expect(modalProps.current?.message).toBe("identity required");
  });

  it("re-attempts checkout after a successful link, in the same mounted panel", async () => {
    // The session hook remembers it already started this purchase, so a naive
    // "clear the error" would leave the user staring at the same refusal.
    createCheckout.mockRejectedValueOnce(new FakeIdentityRequiredError());
    createCheckout.mockResolvedValue(embeddedResponse);

    renderWrapper();
    await waitFor(() => expect(screen.getByTestId("link-identity-modal")).toBeInTheDocument());
    expect(createCheckout).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByText("stub-link"));

    // The count is the assertion: a second session was genuinely requested.
    await waitFor(() => expect(createCheckout).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.getByTestId("stripe-embedded-checkout")).toBeInTheDocument(),
    );
    expect(screen.queryByTestId("link-identity-modal")).not.toBeInTheDocument();
  });

  it("still refuses if the link did not actually take", async () => {
    // The guarantee is in the mutation and runs again on the retry. Linking is
    // not a bypass — if the account is still anonymous, so is the answer.
    createCheckout.mockRejectedValue(new FakeIdentityRequiredError());

    renderWrapper();
    await waitFor(() => expect(screen.getByTestId("link-identity-modal")).toBeInTheDocument());

    fireEvent.click(screen.getByText("stub-link"));

    await waitFor(() => expect(createCheckout).toHaveBeenCalledTimes(2));
    expect(screen.queryByTestId("stripe-embedded-checkout")).not.toBeInTheDocument();
  });

  it("leaves an explanation and a way back when the user closes the modal", async () => {
    createCheckout.mockRejectedValue(new FakeIdentityRequiredError());

    renderWrapper();
    await waitFor(() => expect(screen.getByTestId("link-identity-modal")).toBeInTheDocument());

    fireEvent.click(screen.getByText("stub-dismiss"));

    // Rendering nothing here would look like a broken checkout.
    await waitFor(() =>
      expect(screen.queryByTestId("link-identity-modal")).not.toBeInTheDocument(),
    );
    expect(screen.getByText("identity required")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Add an email/i }));
    expect(screen.getByTestId("link-identity-modal")).toBeInTheDocument();
  });

  it("passes returnTo through for the OAuth path only", async () => {
    createCheckout.mockRejectedValue(new FakeIdentityRequiredError());

    renderWrapper({ returnTo: "/settings/billing?tab=plans" });

    await waitFor(() => expect(modalProps.current).not.toBeNull());
    expect(modalProps.current?.returnTo).toBe("/settings/billing?tab=plans");
  });
});
