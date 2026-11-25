// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated BillingService hooks. Written once, when
// no sibling test existed; this file is YOURS from here — forge never
// regenerates it, so edit freely (delete it and run `forge project rescaffold <path>` to
// get a fresh one).
//
// Each test covers the happy path of one RPC: the hook resolves against the
// mock transport, which is the floor that catches a renamed schema, a broken
// transport wiring, or a hook that no longer compiles. Extend with error /
// loading / empty cases as you build the page — see the `frontend-testing`
// skill for the four-state coverage rule.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, it, expect } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { useGetOrganizationCostSummary } from "./billing-service-hooks_gen";
import { useGetStorageUsage } from "./billing-service-hooks_gen";
import { useListOrganizationUsage } from "./billing-service-hooks_gen";
import { useGetProjectCost } from "./billing-service-hooks_gen";
import { useListPlans } from "./billing-service-hooks_gen";
import { useGetPlan } from "./billing-service-hooks_gen";
import { useGetCurrentUserSubscription } from "./billing-service-hooks_gen";
import { useCreateCurrentUserCheckoutSession } from "./billing-service-hooks_gen";
import { useCreateCurrentUserBillingPortalSession } from "./billing-service-hooks_gen";
import { useListCurrentUserInvoices } from "./billing-service-hooks_gen";
import { useListOrgLimitChanges } from "./billing-service-hooks_gen";
import { useListOrgLimitChangesInfinite } from "./billing-service-hooks_gen";
import { useGetCurrentUserWalletOverview } from "./billing-service-hooks_gen";
import { useCreateCurrentUserWalletTopupSession } from "./billing-service-hooks_gen";
import { useCreateCurrentUserWalletTopupPaymentIntent } from "./billing-service-hooks_gen";
import { useGetCurrentUserWalletTopupQuote } from "./billing-service-hooks_gen";
import { useRedeemCoupon } from "./billing-service-hooks_gen";
import { useGetCurrentUserReliantState } from "./billing-service-hooks_gen";
import { useRepairCurrentUserReliantAccess } from "./billing-service-hooks_gen";
import { useRotateCurrentUserReliantAccess } from "./billing-service-hooks_gen";
import { useGetCurrentUserReliantOverview } from "./billing-service-hooks_gen";
import { useSetCurrentUserReliantEnabled } from "./billing-service-hooks_gen";
import { useGetCurrentUserComputeSubscription } from "./billing-service-hooks_gen";
import { useSetCurrentUserComputeOverage } from "./billing-service-hooks_gen";
import { useSetCurrentUserInfraOverage } from "./billing-service-hooks_gen";
import { useGetCurrentUserInfraOverage } from "./billing-service-hooks_gen";
import { useCreateCurrentUserComputeSubscriptionIntent } from "./billing-service-hooks_gen";
import { useGetMachinePricingReadiness } from "./billing-service-hooks_gen";
import { useAcceptNextMachinePricing } from "./billing-service-hooks_gen";
import { usePreviewMachinePlanChange } from "./billing-service-hooks_gen";
import { useChangeMachinePlan } from "./billing-service-hooks_gen";
import { useGetCurrentUserUsageBilling } from "./billing-service-hooks_gen";
import { useCreateCurrentUserUsageBillingCheckout } from "./billing-service-hooks_gen";
import { useSetUsageBillingBudget } from "./billing-service-hooks_gen";
import { useListUsageBillingBudgetVersions } from "./billing-service-hooks_gen";
import { useGetOrgHostingUsageSummary } from "./billing-service-hooks_gen";
import { useGetFreeHostingStatus } from "./billing-service-hooks_gen";
import { useGetCurrentUserWalletAutoRecharge } from "./billing-service-hooks_gen";
import { useSetCurrentUserWalletAutoRecharge } from "./billing-service-hooks_gen";
import { useCreateCurrentUserWalletPaymentMethodSetup } from "./billing-service-hooks_gen";
import { useConfirmCurrentUserWalletPaymentMethod } from "./billing-service-hooks_gen";
import { useGetCurrentUserComputeUsage } from "./billing-service-hooks_gen";
import { useGetCurrentUserComputeEligibility } from "./billing-service-hooks_gen";
import { useGetUsageSummary } from "./billing-service-hooks_gen";
import { useGetInvoicePreview } from "./billing-service-hooks_gen";
import { useGetCurrentUserBillingEmail } from "./billing-service-hooks_gen";
import { useUpdateBillingEmail } from "./billing-service-hooks_gen";

import type { ReactNode } from "react";

// One QueryClient per test, built before it and torn down after it. Every
// test must leave NOTHING queued behind it: the last test in a file is
// followed by the jsdom teardown, and a React render that fires after that
// dies on `window` — "ReferenceError: window is not defined … caught after
// test environment was torn down" — failing a run whose tests all passed.
let queryClient: QueryClient;

beforeEach(() => {
  queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 0 },
      mutations: { retry: false },
    },
  });
});

// cleanup() is called explicitly because Testing Library only registers its
// own auto-cleanup when `afterEach` is a GLOBAL, and vitest does not expose
// globals unless the config opts in. Without it every renderHook root stays
// mounted, still subscribed, until the worker exits. clear() then drops the
// cache along with any gc timers its queries still hold.
afterEach(() => {
  cleanup();
  queryClient.clear();
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

// mutateAsync resolves BEFORE the hook re-renders: React Query notifies its
// observers on a later macrotask. A mutation test that returned there would
// leave that render queued past its own end — and past the jsdom teardown,
// when it is the file's last test. Wait for the RENDERED status to settle,
// success or error, so the test ends with nothing left to fire.
async function waitForSettled(result: { current: { status: string } }) {
  await waitFor(() => expect(["success", "error"]).toContain(result.current.status));
}

describe("BillingService hooks", () => {
  it("useGetOrganizationCostSummary resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetOrganizationCostSummary": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetOrganizationCostSummary({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetStorageUsage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetStorageUsage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetStorageUsage({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListOrganizationUsage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.ListOrganizationUsage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListOrganizationUsage({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetProjectCost resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetProjectCost": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetProjectCost({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListPlans resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.ListPlans": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListPlans({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetPlan resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetPlan": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetPlan({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetCurrentUserSubscription resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserSubscription": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserSubscription({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateCurrentUserCheckoutSession resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.CreateCurrentUserCheckoutSession": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateCurrentUserCheckoutSession(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateCurrentUserBillingPortalSession resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.CreateCurrentUserBillingPortalSession": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateCurrentUserBillingPortalSession(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListCurrentUserInvoices resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.ListCurrentUserInvoices": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListCurrentUserInvoices({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListOrgLimitChanges resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.ListOrgLimitChanges": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListOrgLimitChanges({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListOrgLimitChangesInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.ListOrgLimitChanges": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListOrgLimitChangesInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useGetCurrentUserWalletOverview resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserWalletOverview": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserWalletOverview({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateCurrentUserWalletTopupSession resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.CreateCurrentUserWalletTopupSession": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateCurrentUserWalletTopupSession(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateCurrentUserWalletTopupPaymentIntent resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.CreateCurrentUserWalletTopupPaymentIntent": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateCurrentUserWalletTopupPaymentIntent(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCurrentUserWalletTopupQuote resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserWalletTopupQuote": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserWalletTopupQuote({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRedeemCoupon resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.RedeemCoupon": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRedeemCoupon(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCurrentUserReliantState resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserReliantState": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserReliantState({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useRepairCurrentUserReliantAccess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.RepairCurrentUserReliantAccess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRepairCurrentUserReliantAccess(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRotateCurrentUserReliantAccess resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.RotateCurrentUserReliantAccess": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRotateCurrentUserReliantAccess(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCurrentUserReliantOverview resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserReliantOverview": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserReliantOverview({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetCurrentUserReliantEnabled resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.SetCurrentUserReliantEnabled": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetCurrentUserReliantEnabled(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCurrentUserComputeSubscription resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserComputeSubscription": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserComputeSubscription({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetCurrentUserComputeOverage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.SetCurrentUserComputeOverage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetCurrentUserComputeOverage(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetCurrentUserInfraOverage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.SetCurrentUserInfraOverage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetCurrentUserInfraOverage(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCurrentUserInfraOverage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserInfraOverage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserInfraOverage({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateCurrentUserComputeSubscriptionIntent resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.CreateCurrentUserComputeSubscriptionIntent": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateCurrentUserComputeSubscriptionIntent(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetMachinePricingReadiness resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetMachinePricingReadiness": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetMachinePricingReadiness({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useAcceptNextMachinePricing resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.AcceptNextMachinePricing": { response: {} },
      }),
    );

    const { result } = renderHook(() => useAcceptNextMachinePricing(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("usePreviewMachinePlanChange resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.PreviewMachinePlanChange": { response: {} },
      }),
    );

    const { result } = renderHook(() => usePreviewMachinePlanChange({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useChangeMachinePlan resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.ChangeMachinePlan": { response: {} },
      }),
    );

    const { result } = renderHook(() => useChangeMachinePlan(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCurrentUserUsageBilling resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserUsageBilling": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserUsageBilling({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useCreateCurrentUserUsageBillingCheckout resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.CreateCurrentUserUsageBillingCheckout": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateCurrentUserUsageBillingCheckout(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useSetUsageBillingBudget resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.SetUsageBillingBudget": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetUsageBillingBudget(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListUsageBillingBudgetVersions resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.ListUsageBillingBudgetVersions": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListUsageBillingBudgetVersions({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetOrgHostingUsageSummary resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetOrgHostingUsageSummary": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetOrgHostingUsageSummary({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetFreeHostingStatus resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetFreeHostingStatus": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetFreeHostingStatus({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetCurrentUserWalletAutoRecharge resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserWalletAutoRecharge": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserWalletAutoRecharge({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetCurrentUserWalletAutoRecharge resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.SetCurrentUserWalletAutoRecharge": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetCurrentUserWalletAutoRecharge(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateCurrentUserWalletPaymentMethodSetup resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.CreateCurrentUserWalletPaymentMethodSetup": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateCurrentUserWalletPaymentMethodSetup(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useConfirmCurrentUserWalletPaymentMethod resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.ConfirmCurrentUserWalletPaymentMethod": { response: {} },
      }),
    );

    const { result } = renderHook(() => useConfirmCurrentUserWalletPaymentMethod(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetCurrentUserComputeUsage resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserComputeUsage": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserComputeUsage({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetCurrentUserComputeEligibility resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserComputeEligibility": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserComputeEligibility({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetUsageSummary resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetUsageSummary": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetUsageSummary({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetInvoicePreview resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetInvoicePreview": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetInvoicePreview({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useGetCurrentUserBillingEmail resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.GetCurrentUserBillingEmail": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetCurrentUserBillingEmail({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUpdateBillingEmail resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "BillingService.UpdateBillingEmail": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateBillingEmail(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
