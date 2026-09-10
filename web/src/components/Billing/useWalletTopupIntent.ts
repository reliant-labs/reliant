/**
 * Mint a top-up PaymentIntent once, and hold onto it.
 *
 * Same discipline as `useCheckoutSession`, which this is the Elements-era
 * counterpart of, and same reason: a panel that mounts and unmounts as a modal
 * opens and closes gets one chance to mint per open. The server reuses the
 * pending row for an identical amount, so a duplicate mint does not double
 * charge — but it does replace a client secret that Elements has already
 * mounted, which remounts the card fields under the user's cursor.
 *
 * So one intent per AMOUNT. Changing the amount is a different purchase and
 * gets a fresh intent; re-rendering is not.
 *
 * ── Why the amount is the key ─────────────────────────────────────────
 *
 * It is the entire purchase decision here — there is no plan, no quantity, no
 * currency choice. That makes the key trivially correct in a way the checkout
 * union's was not, and it lines up exactly with the server's own reuse
 * predicate (`topup.AmountCents == amountCents`), so client and server agree on
 * what "the same purchase" means rather than each having an opinion.
 */

import { useEffect, useRef, useState } from "react";

import {
  isCheckoutIdentityRequired,
  useCreateWalletTopupPaymentIntent,
} from "@/hooks/useCloudBillingQueries";

export type WalletTopupIntentState =
  | { status: "creating" }
  | { status: "ready"; clientSecret: string; topupId: string }
  | { status: "identity_required"; message: string }
  | { status: "error"; message: string };

/** Strips the `[code]` prefix Connect puts in front of the server's message. */
function userFacingMessage(err: unknown): string {
  if (err instanceof Error && err.message) {
    return err.message.replace(/^\[[a-z_]+\]\s*/i, "");
  }
  return "We couldn't start this payment.";
}

export function useWalletTopupIntent(
  amountCents: bigint,
): WalletTopupIntentState {
  const createIntent = useCreateWalletTopupPaymentIntent();
  const [state, setState] = useState<WalletTopupIntentState>({
    status: "creating",
  });

  // A ref, not state: it must update synchronously inside the effect so that
  // StrictMode's double-invoke sees it on the second pass and does not fire a
  // second RPC.
  const startedKey = useRef<string | null>(null);

  // react-query hands back a fresh mutation object every render, so depending
  // on it would re-run this effect forever. Read it through a ref and depend
  // only on the amount, which is what actually identifies the purchase.
  const mutation = useRef(createIntent);
  mutation.current = createIntent;

  const key = String(amountCents);

  useEffect(() => {
    if (startedKey.current === key) return;
    startedKey.current = key;
    setState({ status: "creating" });

    let cancelled = false;

    void (async () => {
      try {
        const response = await mutation.current.mutateAsync(amountCents);
        if (cancelled) return;
        if (!response.paymentIntentClientSecret) {
          // Elements cannot mount without one, and an empty string renders as
          // a silently blank card area rather than an error.
          setState({
            status: "error",
            message: "We couldn't start this payment. Please try again.",
          });
          return;
        }
        setState({
          status: "ready",
          clientSecret: response.paymentIntentClientSecret,
          topupId: response.topup?.id ?? "",
        });
      } catch (err) {
        if (cancelled) return;
        // Cleared so a retry after the user links an identity can run: the
        // amount has not changed, so without this the key check would refuse.
        startedKey.current = null;
        if (isCheckoutIdentityRequired(err)) {
          setState({
            status: "identity_required",
            message: (err as Error).message,
          });
          return;
        }
        setState({ status: "error", message: userFacingMessage(err) });
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [amountCents, key]);

  return state;
}
