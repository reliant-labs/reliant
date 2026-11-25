/**
 * The billing reads moved onto the generated BillingService keys. Two things
 * the old hand-written keys gave for free must still hold:
 *
 *   - an org switch purges them (their root is in ORG_SCOPED_QUERY_ROOTS), or
 *     a switch would show the previous org's wallet and eligibility;
 *   - a write refreshes exactly what it changed. A generated write refreshes
 *     the whole service unless told otherwise, so each billing write names its
 *     scope, and a write that changes nothing a read shows refreshes nothing.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { billingServiceKeys } from "@/hooks/controlplane/billing-service-hooks_gen";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { ORG_SCOPED_QUERY_ROOTS } from "@/store/activeOrgStore";
import { billingHandlers } from "@/test/billingTransport";
import { useCreateBillingPortalSession, useSetComputeOverage } from "../useCloudBillingQueries";

vi.mock("@/store/authStore", () => ({ useAuthStore: { getState: () => ({ user: { is_anonymous: false } }) } }));

const SUBSCRIPTION = billingServiceKeys.getCurrentUserComputeSubscription({});
const EMAIL = billingServiceKeys.getCurrentUserBillingEmail({});

let client: QueryClient;
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  client.setQueryData(SUBSCRIPTION, {});
  client.setQueryData(EMAIL, {});
  setTransport(
    mockTransport(
      billingHandlers({
        setCurrentUserComputeOverage: () => ({}),
        createCurrentUserBillingPortalSession: () => ({ url: "https://billing.stripe.test/portal" }),
      }),
    ),
  );
});
afterEach(() => setTransport(null));

it("an org switch purges every BillingService read", () => {
  expect(ORG_SCOPED_QUERY_ROOTS).toContain(billingServiceKeys.all[0]);
});

it("setting overage refreshes the compute subscription and nothing else", async () => {
  const { result } = renderHook(() => useSetComputeOverage(), { wrapper });
  await result.current.mutateAsync({ enabled: true });
  await waitFor(() => expect(client.getQueryState(SUBSCRIPTION)?.isInvalidated).toBe(true));
  expect(client.getQueryState(EMAIL)?.isInvalidated).toBe(false);
});

it("minting a portal session refreshes nothing", async () => {
  const { result } = renderHook(() => useCreateBillingPortalSession(), { wrapper });
  await result.current.mutateAsync("https://app.test/settings/billing");
  expect(client.getQueryState(SUBSCRIPTION)?.isInvalidated).toBe(false);
  expect(client.getQueryState(EMAIL)?.isInvalidated).toBe(false);
});
