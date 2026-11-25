/**
 * React-Query hooks for the in-app cloud billing dashboard
 * (`/settings/billing`). Every hook runs on the generated BillingService hooks
 * (hooks/controlplane/billing-service-hooks_gen.ts) over the shared
 * control-plane transport — the same interceptor chain (auth, tracing,
 * upgrade/billing-email modal) every other cloud RPC in the app uses.
 *
 * These are the end-user "current user" reads/writes only — never the admin
 * or gateway billing services. Mutations that create Stripe sessions
 * (checkout / top-up / portal) return a hosted URL the caller redirects to;
 * the hook only invalidates the affected cache scopes so a dev-mode
 * same-origin completion (no Stripe redirect) reflects immediately.
 *
 * The writes keep their own signatures and gates (assertPurchaseIdentity, the
 * machine-sale terms check) and call the generated `billingServiceMutations`,
 * each naming exactly the reads it refreshes.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import type { CheckoutUiMode, CreateCurrentUserUsageBillingCheckoutRequestSchema } from "@/gen/controlplane/services/billing/v1/billing_pb";
import type { MessageInitShape } from "@bufbuild/protobuf";
import {
  billingServiceKeys,
  billingServiceMutations,
  billingServiceQueries,
  useGetCurrentUserBillingEmail,
  useGetCurrentUserComputeSubscription,
  useGetCurrentUserWalletTopupQuote,
  useListPlans,
} from "@/hooks/controlplane/billing-service-hooks_gen";
import { refreshing } from "@/hooks/billingKeys";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { useAuthStore } from "@/store/authStore";
import { hasErrorReason } from "@/lib/errorReasons";
import { useMachineSaleTerms, type MachineSaleTerms } from "@/lib/machine-sale-terms";
import { getActiveOrgId } from "@/store/activeOrgStore";
import { orgQueryEnabled, orgRequest, useOrgScope, withOrgFailure } from "@/lib/orgScope";

function billingClient() {
  return getControlPlaneClient(BillingService);
}

/** Candidate sales require versioned commissioned terms, never incumbent-price fallback. */
async function machineSaleRequest(planId: string, consent: MachineSaleTerms | null) {
  const orgId = consent?.orgId ?? getActiveOrgId() ?? undefined;
  if (consent && consent.planId !== planId) throw new Error("Machine plan changed. Review terms again.");
  const currentOrg = getActiveOrgId();
  if (consent && currentOrg && currentOrg !== consent.orgId) throw new Error("Organization changed. Review machine terms again.");
  const ready = await billingClient().getMachinePricingReadiness({ orgId, planId, pricingVersion: consent?.pricingVersion ?? "" });
  if (getActiveOrgId() !== currentOrg) throw new Error("Organization changed. Review machine terms again.");
  if (ready.effectivePeriodStart) throw new Error("Existing subscriptions require separate next-period terms acceptance, not new-subscription checkout.");
  if (!consent) throw new Error("New paid machine checkout is held until commissioned pricing and explicit versioned terms can be verified.");
  if (!ready.activationReady || ready.activationState !== "ready_for_explicit_signup_checkout" || ready.effectivePeriodStart || ready.holdReasons.length || ready.pricingVersion !== consent.pricingVersion || ready.termsSha256 !== consent.termsSha256) throw new Error("Machine terms changed or checkout is held. Review current pricing before authorizing payment.");
  if (getActiveOrgId() && getActiveOrgId() !== consent.orgId) throw new Error("Organization changed. Review machine terms again.");
  return { orgId: consent.orgId, pricingVersion: consent.pricingVersion, termsSha256: consent.termsSha256 };
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

/**
 * Also true for the server's `billing_email_missing`: it means the account
 * has no email to bill (the server falls back from the confirmed billing
 * email to the user's own and then the session's), which is the same ask —
 * so the checkout shows the same account prompt, in place, and retries.
 */
export function isCheckoutIdentityRequired(err: unknown): boolean {
  if (err instanceof CheckoutIdentityRequiredError) return true;
  return (
    err instanceof ConnectError &&
    err.code === Code.InvalidArgument &&
    hasErrorReason(err, "billing_email_missing")
  );
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

// The org-scoped reads (their request has an org_id: the wallet, compute
// usage, invoices, auto-recharge) are read in the tab's org, which their
// request (and so their generated key) names, and not before it is known
// (lib/orgScope). `billingServiceKeys.<method>.all` matches every org's entry,
// for an invalidation.

// ── Queries ───────────────────────────────────────────────────────────

// Independent hosted usage consent; machine checkout remains untouched.
export function useUsageBillingCheckout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: MessageInitShape<typeof CreateCurrentUserUsageBillingCheckoutRequestSchema>) => {
      assertPurchaseIdentity();
      return billingServiceMutations(queryClient).createCurrentUserUsageBillingCheckout(request, { invalidate: false });
    },
  });
}

export function useComputeSubscription() {
  return useGetCurrentUserComputeSubscription({}, { staleTime: 30_000 });
}

export function useWalletOverview() {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      billingServiceQueries.getCurrentUserWalletOverview(orgRequest({ ledgerPage: 1, ledgerPageSize: 5 }, org.orgId), {
        enabled: orgQueryEnabled(org),
        staleTime: 30_000,
      }),
      org,
    ),
  );
}

/**
 * Compute usage for the CURRENT billing period. The server measures only the
 * current period (its balance answers "now"), so there is no period to choose.
 */
export function useComputeUsage(period: "current", { enabled = true }: { enabled?: boolean } = {}) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      billingServiceQueries.getCurrentUserComputeUsage(orgRequest({ period }, org.orgId), {
        enabled: enabled && orgQueryEnabled(org),
        staleTime: 30_000,
      }),
      org,
    ),
  );
}

export function usePlans({ enabled = true }: { enabled?: boolean } = {}) {
  return useListPlans({}, { staleTime: 5 * 60_000, enabled });
}

export function useCurrentUserInvoices() {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      billingServiceQueries.listCurrentUserInvoices(orgRequest({}, org.orgId), {
        enabled: orgQueryEnabled(org),
        staleTime: 60_000,
      }),
      org,
    ),
  );
}

export function useBillingEmail() {
  return useGetCurrentUserBillingEmail({}, { staleTime: 60_000 });
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
      billingServiceMutations(queryClient).setCurrentUserComputeOverage(
        { enabled, budgetCents },
        { invalidate: refreshing(billingServiceKeys.getCurrentUserComputeSubscription.all) },
      ),
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
  const consent = useMachineSaleTerms();
  return useMutation({
    mutationFn: async (args: CheckoutArgs) => {
      assertPurchaseIdentity();
      const terms = await machineSaleRequest(args.planId, consent);
      return billingServiceMutations(queryClient).createCurrentUserCheckoutSession(
        { ...args, ...terms },
        { invalidate: refreshing(billingServiceKeys.getCurrentUserComputeSubscription.all) },
      );
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
      return billingServiceMutations(queryClient).createCurrentUserWalletTopupSession(args, {
        invalidate: refreshing(billingServiceKeys.getCurrentUserWalletOverview.all),
      });
    },
  });
}

/**
 * Mint a PaymentIntent for a wallet top-up, to be confirmed on OUR page.
 *
 * The same `assertPurchaseIdentity` gate as the two session mutations above,
 * for the same reason: this is a thing that spends money, and gating the
 * mutation rather than the button means no call site can bypass it.
 *
 * Nothing is invalidated on success. Unlike the session mutations, minting an
 * intent changes NO server-visible state a query reads — the wallet moves when
 * the webhook credits it, which is a different moment and is what
 * `useWalletSettlement` waits for. Invalidating here would refetch a balance
 * that cannot have changed yet and teach the UI to expect it.
 */
export function useCreateWalletTopupPaymentIntent() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (amountCents: bigint) => {
      assertPurchaseIdentity();
      return billingServiceMutations(queryClient).createCurrentUserWalletTopupPaymentIntent({ amountCents }, { invalidate: false });
    },
  });
}

/**
 * Mint a compute-subscription intent for our own Elements checkout.
 *
 * Deliberately does NOT invalidate the compute-subscription query, for the
 * same reason the top-up intent does not invalidate the wallet: creating an
 * intent changes no server state a query reads. The subscription exists at
 * Stripe in `incomplete` status, which is not an entitlement — the plan moves
 * when the webhook confirms payment. Invalidating here would refetch a
 * subscription that cannot have changed yet and teach the UI to expect one.
 */
export function useCreateComputeSubscriptionIntent() {
  const queryClient = useQueryClient();
  const consent = useMachineSaleTerms();
  return useMutation({
    mutationFn: async (planId: string) => {
      assertPurchaseIdentity();
      const terms = await machineSaleRequest(planId, consent);
      return billingServiceMutations(queryClient).createCurrentUserComputeSubscriptionIntent({ planId, ...terms }, { invalidate: false });
    },
  });
}

/**
 * What a top-up costs: credit, service fee, and the charged total.
 *
 * THE FEE IS NEVER COMPUTED HERE. It arrives from the server, which derives it
 * from the same function that builds the Stripe charge — so the number the user
 * reads before paying and the number their card is charged cannot disagree. A
 * percentage applied in this file would be a second implementation of the rule,
 * free to drift, and one a user could edit.
 *
 * Quoting mints nothing at Stripe and writes no row, so the amount picker may
 * call it on every preset the user clicks. Amounts below the $5 minimum are not
 * quotable and the server rejects them; `enabled` keeps that out of the UI as a
 * failed request.
 */
export function useWalletTopupQuote(creditCents: number, enabled = true) {
  // The request is built on every render (it is the key), so a fractional
  // amount, which BigInt() throws on, is never quotable rather than a crash.
  const quotable = Number.isSafeInteger(creditCents) && creditCents > 0;
  return useGetCurrentUserWalletTopupQuote(
    { creditCents: quotable ? BigInt(creditCents) : 0n },
    {
      enabled: enabled && quotable,
      // A price, not a balance: it changes when we change the rate, which is a
      // deploy, not a user action.
      staleTime: 5 * 60_000,
    },
  );
}

/** The caller's auto-recharge rule and saved card, if any. */
export function useWalletAutoRecharge(enabled = true) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      billingServiceQueries.getCurrentUserWalletAutoRecharge(orgRequest({}, org.orgId), {
        enabled: enabled && orgQueryEnabled(org),
        staleTime: 30_000,
      }),
      org,
    ),
  );
}

/**
 * Arm or disarm auto-recharge.
 *
 * The request REPLACES the stored rule — threshold, amount and ceiling travel
 * together — so callers must send the user's whole current choice, not a patch.
 */
export function useSetWalletAutoRecharge() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (rule: {
      enabled: boolean;
      thresholdCents: number;
      amountCents: number;
      maxPerMonthCents: number;
    }) => {
      assertPurchaseIdentity();
      return billingServiceMutations(queryClient).setCurrentUserWalletAutoRecharge(
        {
          enabled: rule.enabled,
          thresholdCents: BigInt(rule.thresholdCents),
          amountCents: BigInt(rule.amountCents),
          maxPerMonthCents: BigInt(rule.maxPerMonthCents),
        },
        { invalidate: refreshing(billingServiceKeys.getCurrentUserWalletAutoRecharge.all) },
      );
    },
  });
}

/**
 * Begin saving a card for off-session use. Moves no money.
 *
 * Returns EITHER an already-saved card (adopted from Stripe — the path every
 * compute subscriber takes, where this is a read rather than a collection) OR a
 * SetupIntent client secret to mount with Stripe.js. Never both, so the caller
 * branches on which one arrived.
 */
export function useCreateWalletPaymentMethodSetup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => {
      assertPurchaseIdentity();
      return billingServiceMutations(queryClient).createCurrentUserWalletPaymentMethodSetup(
        {},
        { invalidate: refreshing(billingServiceKeys.getCurrentUserWalletAutoRecharge.all) },
      );
    },
  });
}

export function useCreateBillingPortalSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (returnUrl: string) =>
      billingServiceMutations(queryClient).createCurrentUserBillingPortalSession({ returnUrl }, { invalidate: false }),
  });
}

export function useUpdateBillingEmail() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (email: string) =>
      billingServiceMutations(queryClient).updateBillingEmail(
        { email },
        { invalidate: refreshing(billingServiceKeys.getCurrentUserBillingEmail.all) },
      ),
  });
}
