/**
 * Create a checkout session once, and hold onto it.
 *
 * ── Why this is a hook and not an inline `useEffect` ──────────────────
 *
 * The panel can be mounted, unmounted and remounted as a modal opens and
 * closes, and each mount is a chance to mint another Stripe session. That was
 * survivable with the hosted redirect — the page went away — but a modal makes
 * open/close cheap and repeatable, so the naive version mints a session per
 * open. The backend now derives idempotency keys, which stops a double CHARGE;
 * it does not stop a pile of abandoned sessions racing the 24h reaper, and it
 * does not stop the panel flickering as a second secret replaces the first.
 *
 * So the request is created ONCE per distinct purchase, keyed by what is being
 * bought. Re-renders reuse it; changing the target starts a new one.
 */

import { useEffect, useRef, useState } from "react";

import { CheckoutUiMode } from "@/gen/controlplane/v1/public/billing_service_pb";
import {
  isCheckoutIdentityRequired,
  useCreateCheckoutSession,
  useCreateWalletTopupSession,
} from "@/hooks/useCloudBillingQueries";
import {
  selectCheckoutPresentation,
  type CheckoutPresentation,
} from "@/lib/stripeCheckout";

/**
 * What the caller wants to buy.
 *
 * A discriminated union rather than a bag of optional fields, so "a compute
 * plan with an amount" and "a top-up with a plan id" are not expressible.
 */
export type CheckoutRequest =
  | { kind: "compute_plan"; planId: string }
  | { kind: "wallet_topup"; amountCents: bigint };

export type CheckoutSessionState =
  | { status: "creating" }
  | { status: "ready"; presentation: CheckoutPresentation }
  | { status: "identity_required"; message: string }
  | { status: "error"; message: string };

/** Stable identity for a purchase target — the reuse key. */
function requestKey(request: CheckoutRequest): string {
  return request.kind === "compute_plan"
    ? `compute_plan:${request.planId}`
    : `wallet_topup:${request.amountCents}`;
}

export function useCheckoutSession(
  request: CheckoutRequest,
): CheckoutSessionState {
  const checkoutMutation = useCreateCheckoutSession();
  const topupMutation = useCreateWalletTopupSession();
  const [state, setState] = useState<CheckoutSessionState>({
    status: "creating",
  });

  // The key of the request we have already started. A ref, not state: it must
  // be updated synchronously during the effect so a second effect run in the
  // same commit (StrictMode's double-invoke, most obviously) sees it and does
  // not fire a second RPC.
  const startedKey = useRef<string | null>(null);

  // The mutations come from react-query and are fresh objects each render, so
  // depending on them would re-run this effect forever. Read them through a
  // ref instead and depend only on the key, which is the thing that actually
  // identifies a purchase.
  const mutations = useRef({ checkoutMutation, topupMutation });
  mutations.current = { checkoutMutation, topupMutation };

  const key = requestKey(request);

  useEffect(() => {
    if (startedKey.current === key) return;
    startedKey.current = key;
    setState({ status: "creating" });

    let cancelled = false;

    const create = async () => {
      try {
        // Embedded mode takes NO redirect URLs: Stripe rejects
        // success_url/cancel_url outright when ui_mode is embedded, and
        // return_url is only consulted by bank-redirect payment methods —
        // never by cards or wallets. Omitting it keeps the checkout path off
        // ALLOWED_REDIRECT_HOSTS entirely, which is where a recurring class of
        // production outage lived.
        const response =
          request.kind === "compute_plan"
            ? await mutations.current.checkoutMutation.mutateAsync({
                planId: request.planId,
                uiMode: CheckoutUiMode.EMBEDDED,
                successUrl: "",
                cancelUrl: "",
              })
            : await mutations.current.topupMutation.mutateAsync({
                amountCents: request.amountCents,
                uiMode: CheckoutUiMode.EMBEDDED,
                successUrl: "",
                cancelUrl: "",
              });

        if (cancelled) return;
        setState({
          status: "ready",
          presentation: selectCheckoutPresentation(response),
        });
      } catch (err) {
        if (cancelled) return;
        // The anti-anonymous-purchase guarantee, surfaced rather than
        // swallowed. It fired inside the mutation, before any session existed
        // — which is the point: there is nothing to clean up.
        if (isCheckoutIdentityRequired(err)) {
          startedKey.current = null; // let a retry after linking work
          setState({
            status: "identity_required",
            message: (err as Error).message,
          });
          return;
        }
        startedKey.current = null;
        setState({
          status: "error",
          message:
            err instanceof Error ? err.message : "Failed to start checkout",
        });
      }
    };

    void create();
    return () => {
      cancelled = true;
    };
  }, [key, request]);

  return state;
}
