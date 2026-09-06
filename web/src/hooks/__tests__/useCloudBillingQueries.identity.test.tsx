/**
 * The anonymous-checkout guarantee, enforced where it actually matters.
 *
 * A subscription bought against an anonymous browser session belongs to nobody
 * reachable — losing the session loses the purchase. That rule used to live in
 * `useGoToBilling`, i.e. at five NAVIGATION call sites, any one of which could
 * be added without it. These tests pin it to the checkout MUTATION instead:
 * whatever route a user took to get to a Subscribe button, the session cannot
 * be minted.
 */
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const createCheckout = vi.fn();
const createTopup = vi.fn();

vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({
    createCurrentUserCheckoutSession: createCheckout,
    createCurrentUserWalletTopupSession: createTopup,
  }),
}));

type MockUser = { is_anonymous?: boolean } | null;
const authState = vi.hoisted(() => ({ user: null as MockUser }));

vi.mock("@/store/authStore", () => {
  const useAuthStore = (selector: (s: { user: MockUser }) => unknown) =>
    selector({ user: authState.user });
  useAuthStore.getState = () => ({ user: authState.user });
  return { useAuthStore };
});

import {
  CheckoutIdentityRequiredError,
  useCreateCheckoutSession,
  useCreateWalletTopupSession,
} from "../useCloudBillingQueries";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  vi.clearAllMocks();
  authState.user = null;
  createCheckout.mockResolvedValue({ checkoutUrl: "https://checkout.stripe.com/x" });
  createTopup.mockResolvedValue({ checkoutUrl: "https://checkout.stripe.com/y" });
});

describe("checkout identity guard", () => {
  it("refuses to mint a checkout session for an anonymous user", async () => {
    authState.user = { is_anonymous: true };

    const { result } = renderHook(() => useCreateCheckoutSession(), { wrapper });

    await expect(
      result.current.mutateAsync({
        planId: "compute-standard",
        successUrl: "https://app.example.test/settings/billing?checkout=success",
        cancelUrl: "https://app.example.test/settings/billing?checkout=cancelled",
      }),
    ).rejects.toBeInstanceOf(CheckoutIdentityRequiredError);

    // The RPC is never reached — the guarantee is structural, not a UI choice.
    expect(createCheckout).not.toHaveBeenCalled();
  });

  it("refuses a wallet top-up for an anonymous user too", async () => {
    authState.user = { is_anonymous: true };

    const { result } = renderHook(() => useCreateWalletTopupSession(), { wrapper });

    await expect(
      result.current.mutateAsync({
        amountCents: 2500n,
        successUrl: "https://app.example.test/settings/billing?checkout=success",
        cancelUrl: "https://app.example.test/settings/billing?checkout=cancelled",
      }),
    ).rejects.toBeInstanceOf(CheckoutIdentityRequiredError);

    expect(createTopup).not.toHaveBeenCalled();
  });

  it("lets a signed-in user through to Stripe", async () => {
    authState.user = { is_anonymous: false };

    const { result } = renderHook(() => useCreateCheckoutSession(), { wrapper });

    await result.current.mutateAsync({
      planId: "compute-standard",
      successUrl: "https://app.example.test/settings/billing?checkout=success",
      cancelUrl: "https://app.example.test/settings/billing?checkout=cancelled",
    });

    await waitFor(() => expect(createCheckout).toHaveBeenCalledTimes(1));
  });

  // api-key / mock / dev synthetic users set is_anonymous: false, and a session
  // with no user at all is not an anonymous SUPABASE session. Same rule as
  // useGoToBilling used to apply: ONLY is_anonymous === true counts.
  it("treats a user with no is_anonymous flag as signed in", async () => {
    authState.user = {};

    const { result } = renderHook(() => useCreateCheckoutSession(), { wrapper });

    await result.current.mutateAsync({
      planId: "compute-standard",
      successUrl: "https://app.example.test/settings/billing?checkout=success",
      cancelUrl: "https://app.example.test/settings/billing?checkout=cancelled",
    });

    await waitFor(() => expect(createCheckout).toHaveBeenCalledTimes(1));
  });
});
