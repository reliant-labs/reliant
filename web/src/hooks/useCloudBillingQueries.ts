/**
 * React-Query hooks for the in-app cloud billing dashboard
 * (`/settings/billing`). Every hook talks to the PUBLIC
 * `controlplane.v1.BillingService` through the shared control-plane transport
 * (`getControlPlaneClient`) — the same interceptor chain (auth, tracing,
 * upgrade/billing-email modal) every other cloud RPC in the app uses.
 *
 * These are the end-user "current user" reads/writes only — never the admin
 * or gateway billing services. Mutations that create Stripe sessions
 * (checkout / top-up / portal) return a hosted URL the caller redirects to;
 * the hook only invalidates the affected cache scopes so a dev-mode
 * same-origin completion (no Stripe redirect) reflects immediately.
 */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { BillingService } from "@/gen/controlplane/v1/public/billing_service_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";

function billingClient() {
  return getControlPlaneClient(BillingService);
}

export const cloudBillingKeys = {
  all: ["cloud-billing"] as const,
  computeSubscription: ["cloud-billing", "compute-subscription"] as const,
  walletOverview: ["cloud-billing", "wallet-overview"] as const,
  computeUsage: (period: string) =>
    ["cloud-billing", "compute-usage", period] as const,
  plans: ["cloud-billing", "plans"] as const,
  invoices: ["cloud-billing", "invoices"] as const,
  billingEmail: ["cloud-billing", "billing-email"] as const,
};

// ── Queries ───────────────────────────────────────────────────────────

export function useComputeSubscription() {
  return useQuery({
    queryKey: cloudBillingKeys.computeSubscription,
    queryFn: () => billingClient().getCurrentUserComputeSubscription({}),
    staleTime: 30_000,
  });
}

export function useWalletOverview() {
  return useQuery({
    queryKey: cloudBillingKeys.walletOverview,
    queryFn: () =>
      billingClient().getCurrentUserWalletOverview({
        ledgerPage: 1,
        ledgerPageSize: 5,
      }),
    staleTime: 30_000,
  });
}

export function useComputeUsage(period: "current" | "previous") {
  return useQuery({
    queryKey: cloudBillingKeys.computeUsage(period),
    queryFn: () => billingClient().getCurrentUserComputeUsage({ period }),
    staleTime: 30_000,
  });
}

export function usePlans() {
  return useQuery({
    queryKey: cloudBillingKeys.plans,
    queryFn: () => billingClient().listPlans({}),
    staleTime: 5 * 60_000,
  });
}

export function useCurrentUserInvoices() {
  return useQuery({
    queryKey: cloudBillingKeys.invoices,
    queryFn: () => billingClient().listCurrentUserInvoices({}),
    staleTime: 60_000,
  });
}

export function useBillingEmail() {
  return useQuery({
    queryKey: cloudBillingKeys.billingEmail,
    queryFn: () => billingClient().getCurrentUserBillingEmail({}),
    staleTime: 60_000,
  });
}

// ── Mutations ─────────────────────────────────────────────────────────

export function useSetComputeOverage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) =>
      billingClient().setCurrentUserComputeOverage({ enabled }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: cloudBillingKeys.computeSubscription,
      });
    },
  });
}

export interface CheckoutArgs {
  planId: string;
  successUrl: string;
  cancelUrl: string;
}

export function useCreateCheckoutSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args: CheckoutArgs) =>
      billingClient().createCurrentUserCheckoutSession(args),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: cloudBillingKeys.computeSubscription,
      });
    },
  });
}

export interface TopupArgs {
  amountCents: bigint;
  successUrl: string;
  cancelUrl: string;
}

export function useCreateWalletTopupSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args: TopupArgs) =>
      billingClient().createCurrentUserWalletTopupSession(args),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: cloudBillingKeys.walletOverview,
      });
    },
  });
}

export function useCreateBillingPortalSession() {
  return useMutation({
    mutationFn: (returnUrl: string) =>
      billingClient().createCurrentUserBillingPortalSession({ returnUrl }),
  });
}

export function useUpdateBillingEmail() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (email: string) =>
      billingClient().updateBillingEmail({ email }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: cloudBillingKeys.billingEmail,
      });
    },
  });
}
