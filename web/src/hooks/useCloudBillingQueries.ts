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
import type {
  CheckoutUiMode,
  GetCurrentUserComputeUsageResponse,
} from "@/gen/controlplane/v1/public/billing_service_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { useAuthStore } from "@/store/authStore";

function billingClient() {
  return getControlPlaneClient(BillingService);
}

/**
 * Thrown instead of minting a Stripe session for a session that has no real
 * identity behind it.
 *
 * WHY IT LIVES HERE and not at the buttons: a subscription bought against an
 * anonymous browser session belongs to nobody reachable, and losing the session
 * loses the purchase along with the work. That rule used to be enforced by
 * `useGoToBilling` refusing to NAVIGATE an anonymous user to billing — one
 * chokepoint, but the wrong one: it guarded five navigation call sites, and a
 * sixth added without it would have dead-ended silently. Gating the mutation
 * gates the only thing that actually spends money, so no call site can bypass
 * it by construction.
 *
 * Callers render the identity-link affordance; they do not decide the rule.
 */
export class CheckoutIdentityRequiredError extends Error {
  constructor() {
    super(
      "Before we take payment we need an account we can reach — a subscription tied to this browser session would be lost with it.",
    );
    this.name = "CheckoutIdentityRequiredError";
  }
}

export function isCheckoutIdentityRequired(err: unknown): boolean {
  return err instanceof CheckoutIdentityRequiredError;
}

/**
 * Only genuine anonymous Supabase sessions carry `is_anonymous === true`;
 * api-key / mock / dev synthetic users set it false and must not be pushed
 * through an identity link they cannot complete. Read imperatively (not via the
 * hook selector) so the check runs against the session as it stands at the
 * moment of purchase, not whatever was current when the component mounted.
 */
function assertPurchaseIdentity(): void {
  const user = useAuthStore.getState().user;
  if (user && (user as { is_anonymous?: boolean }).is_anonymous === true) {
    throw new CheckoutIdentityRequiredError();
  }
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

/**
 * Compute usage, with `granted_minutes_remaining` narrowed from the wire's
 * bigint (int64) to a plain number so it reads like the neighbouring minute
 * fields. Hoisted to module scope so its identity is stable — an inline
 * `select` would produce a fresh result object on every render and defeat the
 * `useMemo` deps at the call sites.
 */
export type ComputeUsage = Omit<
  GetCurrentUserComputeUsageResponse,
  "grantedMinutesRemaining"
> & { grantedMinutesRemaining: number };

function selectComputeUsage(
  data: GetCurrentUserComputeUsageResponse,
): ComputeUsage {
  return { ...data, grantedMinutesRemaining: Number(data.grantedMinutesRemaining) };
}

export function useComputeUsage(period: "current" | "previous") {
  return useQuery({
    queryKey: cloudBillingKeys.computeUsage(period),
    queryFn: () => billingClient().getCurrentUserComputeUsage({ period }),
    select: selectComputeUsage,
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

/**
 * The caller's whole overage decision: whether machines may run past included
 * hours, and the monthly ceiling on what that may cost.
 *
 * `budgetCents` omitted (or 0) means NO CAP, matching the server's `> 0` test.
 * The request REPLACES the stored cap rather than patching it, so send the
 * user's current choice on every call — not only when the number changes.
 *
 * Both halves travel together so "overage on, no ceiling" has to be picked
 * rather than passed through on the way to setting a limit.
 *
 * This mutation authorizes additional spend. Call it only from an explicit
 * user action — never from an effect or from session setup.
 */
export interface ComputeOverageSettings {
  enabled: boolean;
  budgetCents?: bigint;
}

export function useSetComputeOverage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ enabled, budgetCents }: ComputeOverageSettings) =>
      billingClient().setCurrentUserComputeOverage({ enabled, budgetCents }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: cloudBillingKeys.computeSubscription,
      });
    },
  });
}

export interface CheckoutArgs {
  planId: string;
  /**
   * Hosted mode only — Stripe REJECTS these outright when ui_mode is embedded.
   * Send empty strings alongside `uiMode: EMBEDDED`.
   */
  successUrl: string;
  cancelUrl: string;
  /** Unset means HOSTED, so existing call sites are unchanged. */
  uiMode?: CheckoutUiMode;
  /**
   * Embedded mode only, and almost always omitted: only bank-redirect payment
   * methods consult it, never cards or wallets. Leaving it out keeps embedded
   * checkout off the ALLOWED_REDIRECT_HOSTS path entirely.
   */
  returnUrl?: string;
}

export function useCreateCheckoutSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args: CheckoutArgs) => {
      assertPurchaseIdentity();
      return billingClient().createCurrentUserCheckoutSession(args);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: cloudBillingKeys.computeSubscription,
      });
    },
  });
}

export interface TopupArgs {
  amountCents: bigint;
  /** Hosted mode only; see CheckoutArgs. */
  successUrl: string;
  cancelUrl: string;
  uiMode?: CheckoutUiMode;
  returnUrl?: string;
}

export function useCreateWalletTopupSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args: TopupArgs) => {
      assertPurchaseIdentity();
      return billingClient().createCurrentUserWalletTopupSession(args);
    },
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
