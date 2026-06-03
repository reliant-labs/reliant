/**
 * Upgrade-modal Connect interceptor — shared by every Connect transport in the
 * app (`api/grpc-client.ts` and `services/controlPlane/client.ts`).
 *
 * Background: the backend signals "quota hit, please upgrade" by returning
 * Connect `ResourceExhausted` with two response headers attached:
 *   - `x-reliant-reason`      machine-readable reason code (e.g.
 *                             "free_tier_compute_minutes")
 *   - `x-reliant-upgrade-url` optional path/URL to the upgrade page
 *
 * The UI translates that into an `UpgradeRequiredModal` opened via the
 * `useModalStore`. Without this interceptor on a given transport, the user
 * sees only a raw error toast on that transport's calls — which is exactly
 * how the project picker's "Resume daemon" button leaked before this module
 * existed: it routes through `services/controlPlane/client.ts`, which built
 * its own transport with only an auth interceptor, so `ResourceExhausted`
 * from the per-org compute cap surfaced as toast text instead of the modal.
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

let _upgradeModalInFlight = false;

export const upgradeInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (error) {
    if (
      !(error instanceof ConnectError) ||
      error.code !== Code.ResourceExhausted
    ) {
      throw error;
    }
    const reason = error.metadata.get(RELIANT_REASON_HEADER) ?? "";
    if (!reason || _upgradeModalInFlight) {
      throw error;
    }
    const upgradeUrl = error.metadata.get(RELIANT_UPGRADE_URL_HEADER) ?? "";

    _upgradeModalInFlight = true;
    try {
      const { useModalStore } = await import("../store/modalStore");
      useModalStore.getState().openModal("upgrade-required", {
        reason,
        message: error.rawMessage || error.message,
        upgradeUrl,
      });
      // Reset the single-fire guard when the modal closes so a later quota
      // hit (different feature) opens a new one.
      const unsub = useModalStore.subscribe((s) => {
        if (s.activeModal !== "upgrade-required") {
          _upgradeModalInFlight = false;
          unsub();
        }
      });
    } catch (modalErr) {
      logger.error("[upgradeInterceptor] Failed to open upgrade modal", modalErr);
      _upgradeModalInFlight = false;
    }
    throw error;
  }
};

/**
 * Test-only reset. Vitest suites that mount the modal store can call this
 * between cases to make sure the single-fire guard doesn't leak across tests.
 */
export function __resetUpgradeModalGuardForTests(): void {
  _upgradeModalInFlight = false;
}
