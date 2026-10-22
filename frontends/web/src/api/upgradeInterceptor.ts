/**
 * Backend-signalled-modal Connect interceptor — shared by every Connect
 * transport in the app (`api/grpc-client.ts` and
 * `services/controlPlane/client.ts`).
 *
 * Background: control-plane signals two kinds of "UI should intervene"
 * conditions via response headers on Connect errors:
 *
 *   1. Quota exhausted (Code.ResourceExhausted, x-reliant-reason set)
 *      -> opens UpgradeRequiredModal with the reason + optional upgrade URL.
 *   2. Billing email missing (Code.InvalidArgument,
 *      x-reliant-reason=billing_email_missing)
 *      -> opens BillingEmailRequiredModal so the user can call
 *      BillingService.UpdateBillingEmail and retry.
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
import { logger } from "../lib/logger";

const RELIANT_REASON_HEADER = "x-reliant-reason";
const RELIANT_UPGRADE_URL_HEADER = "x-reliant-upgrade-url";

// Stable contract with control-plane; mirrors
// internal/service/svcbilling/connecterr.go.
const BILLING_EMAIL_MISSING_REASON = "billing_email_missing";

let _backendModalInFlight = false;

export const upgradeInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (error) {
    if (!(error instanceof ConnectError)) {
      throw error;
    }
    const reason = error.metadata.get(RELIANT_REASON_HEADER) ?? "";

    if (
      error.code === Code.InvalidArgument &&
      reason === BILLING_EMAIL_MISSING_REASON
    ) {
      if (!_backendModalInFlight) {
        openModalOnce("billing-email-required", {
          message: error.rawMessage || error.message,
        });
      }
      throw error;
    }

    if (error.code === Code.ResourceExhausted && reason) {
      if (!_backendModalInFlight) {
        const upgradeUrl = error.metadata.get(RELIANT_UPGRADE_URL_HEADER) ?? "";
        openModalOnce("upgrade-required", {
          reason,
          message: error.rawMessage || error.message,
          upgradeUrl,
        });
      }
      throw error;
    }

    throw error;
  }
};

function openModalOnce<K extends "upgrade-required" | "billing-email-required">(
  id: K,
  // Lazy-typed because we can't import the heavy modalStore types here
  // without creating an eager dependency cycle (modalStore -> components
  // -> transport -> interceptor -> modalStore).
  data: unknown,
): void {
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
