/**
 * Backend-signalled-modal Connect interceptor — shared by every Connect
 * transport in the app (every route `lib/connect.ts` builds, through
 * `buildInterceptors` in `api/transport.ts`).
 *
 * Background: control-plane signals two kinds of "UI should intervene"
 * conditions via the reason on Connect errors (lib/errorReasons.ts):
 *
 *   1. Quota exhausted (Code.ResourceExhausted with a reason)
 *      -> opens UpgradeRequiredModal with the reason; it takes the payment
 *      in place and then offers to repeat the refused request.
 *   2. Billing email missing (Code.InvalidArgument, reason
 *      billing_email_missing): the account has no email — the backend falls
 *      back from the confirmed billing email to the user's own and then the
 *      session's — so the fix is the one account prompt. UpgradeRequiredModal
 *      renders LinkIdentityModal for it and re-runs the request on its own
 *      once the account has an email.
 *
 * Both carry `retry`: the refused request, re-issued. A unary request is
 * self-contained, so sending it again is exactly "do what the user asked,
 * now that the refusal is fixed". It runs only behind an explicit click after
 * a payment (the request may be billable); after an account link it runs by
 * itself, because the requests that need an email create checkout intents,
 * which charge nothing.
 *
 * The checkout surfaces' own intent calls (`CreateCurrentUser*`) are left to
 * them: they render the account prompt in place (isCheckoutIdentityRequired)
 * and remount to retry, so a global modal on top would ask twice.
 *
 * Without this interceptor on a given transport, the user sees only a raw
 * error toast on that transport's calls — which is exactly how the project
 * picker's "Resume daemon" button leaked before this module existed: it
 * routed through `services/controlPlane/client.ts`, which built its own
 * transport with only an auth interceptor, so `ResourceExhausted` from the
 * per-org compute cap surfaced as toast text instead of the modal.
 *
 * The interceptor RE-THROWS the error after opening the modal — callers'
 * catch/try/finally semantics stay intact (mutations roll back, retries
 * don't fire, error states render). The modal is a UX overlay, not error
 * recovery.
 *
 * Single-fire guard: a burst of failed RPCs (e.g. a dashboard that fires N
 * queries on mount) opens exactly one modal, not N. The guard is module-level
 * so it spans every transport that shares this interceptor — the user can't
 * accidentally stack two modals by hitting two transports in parallel.
 */

import { ConnectError, Code } from "@connectrpc/connect";
import type { Interceptor } from "@connectrpc/connect";
import { ENTITLEMENT_REASONS, errorReason, hasErrorReason } from "../lib/errorReasons";
import { logger } from "../lib/logger";

let _backendModalInFlight = false;

export const upgradeInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (error) {
    if (!(error instanceof ConnectError)) {
      throw error;
    }
    if (
      error.code === Code.InvalidArgument &&
      hasErrorReason(error, "billing_email_missing")
    ) {
      if (!_backendModalInFlight && !isCheckoutIntent(req.method.name)) {
        openModalOnce({
          reason: "billing_email_missing",
          message: error.rawMessage || error.message,
          retry: retryOf(next, req),
        });
      }
      throw error;
    }

    if (error.code === Code.ResourceExhausted && hasErrorReason(error, ENTITLEMENT_REASONS)) {
      if (!_backendModalInFlight) {
        openModalOnce({
          reason: errorReason(error) ?? "",
          message: error.rawMessage || error.message,
          retry: retryOf(next, req),
          retryLabel: RETRY_LABELS[req.method.name],
        });
      }
      throw error;
    }

    throw error;
  }
};

/** The checkout surfaces' intent RPCs, which handle the account ask themselves. */
function isCheckoutIntent(method: string | undefined): boolean {
  return !!method && method.startsWith("CreateCurrentUser");
}

/** What the retry button says, for the requests whose action has a name. */
const RETRY_LABELS: Record<string, string> = {
  ResumeDaemon: "Start my machine",
  CreateDaemon: "Create the machine",
  ResizeDaemon: "Resize the machine",
  GrowDaemonDisk: "Grow the disk",
};

type Next = Parameters<Interceptor>[0];
type Req = Parameters<Next>[0];

/** Re-issue a refused unary request. Streams cannot be replayed; none is offered. */
function retryOf(next: Next, req: Req): (() => Promise<unknown>) | undefined {
  if (req.stream) return undefined;
  return () => next(req);
}

const UPGRADE_MODAL = "upgrade-required";

function openModalOnce(
  // Lazy-typed because we can't import the heavy modalStore types here
  // without creating an eager dependency cycle (modalStore -> components
  // -> transport -> interceptor -> modalStore).
  data: unknown,
): void {
  const id = UPGRADE_MODAL;
  _backendModalInFlight = true;
  void (async () => {
    try {
      const { useModalStore } = await import("../store/modalStore");
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      useModalStore.getState().openModal(id, data as any);
      // Reset the single-fire guard when this modal closes so a later
      // event (different feature, possibly different modal) opens fresh.
      const unsub = useModalStore.subscribe((s) => {
        if (s.activeModal !== id) {
          _backendModalInFlight = false;
          unsub();
        }
      });
    } catch (modalErr) {
      logger.error(
        "[upgradeInterceptor] Failed to open backend-signalled modal",
        modalErr,
      );
      _backendModalInFlight = false;
    }
  })();
}

/**
 * Test-only reset. Vitest suites that mount the modal store can call this
 * between cases to make sure the single-fire guard doesn't leak across tests.
 */
export function __resetUpgradeModalGuardForTests(): void {
  _backendModalInFlight = false;
}
