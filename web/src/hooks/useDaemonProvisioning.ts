import { useEffect, useState } from "react";

/**
 * Whether a local daemon is running but not yet registered with the
 * control-plane, so it cannot appear in ListDaemons yet.
 *
 * ── The gap this closes ───────────────────────────────────────────────
 *
 * A packaged desktop build ships its own daemon, but that daemon has NO
 * credentials until the user signs in. The sequence after a fresh sign-in is:
 *
 *   t+0s   sign-in completes, renderer navigates to /onboarding
 *   t+0s   daemon is still "awaiting credentials" — not registered
 *   t+11s  auth:save fires, daemon restarts with the new principal
 *   t+15s  gateway registers it; it finally appears in ListDaemons
 *
 * For those ~15 seconds ListDaemons correctly returns an empty list, and the
 * onboarding compute step reads that as "this user has no daemon" and asks
 * them to choose their compute. The question answers itself moments later,
 * but by then the user has usually clicked — and the step's `hasAdvanced`
 * latch means the auto-skip effect can never fire afterwards.
 *
 * An empty daemon list is therefore ambiguous on desktop, and this hook is
 * what disambiguates it: "no daemon" versus "a daemon that is seconds away".
 *
 * ── Why polling rather than an event ──────────────────────────────────
 *
 * window.RELIANT_CONFIG is injected at load and refreshed only on
 * `backend-port` events, which do not fire for a credential change — the port
 * is identical before and after. Rather than add an IPC event whose only
 * consumer is this hook, we re-read the status the preload already exposes.
 * The poll is short-lived by construction: it stops as soon as the daemon
 * stops awaiting credentials, which is a one-way transition per sign-in.
 *
 * Returns false everywhere except a packaged desktop build with a daemon
 * actually mid-provisioning, so web callers get today's behaviour unchanged.
 */

const POLL_INTERVAL_MS = 1000;

// Bounds the wait so a daemon that never credentials (a crash, a revoked
// token) cannot pin onboarding on a spinner forever. Comfortably longer than
// the ~15s observed end-to-end, short enough that a stuck daemon still lets
// the user reach the compute choice and pick cloud instead.
const MAX_WAIT_MS = 45_000;

type BackendStatus = { awaitingCredentials?: boolean };

const readStatus = async (): Promise<BackendStatus | null> => {
  const api = (window as unknown as {
    electronAPI?: { getBackendStatus?: () => Promise<BackendStatus> };
  }).electronAPI;
  if (!api?.getBackendStatus) return null;
  try {
    return await api.getBackendStatus();
  } catch {
    // A failed status read must not strand onboarding: treat it as "not
    // provisioning" so the user still gets a usable step.
    return null;
  }
};

export function useDaemonProvisioning(): boolean {
  const [provisioning, setProvisioning] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const startedAt = Date.now();

    const tick = async () => {
      const status = await readStatus();
      if (cancelled) return;

      const awaiting = Boolean(status?.awaitingCredentials);
      // Give up waiting once the daemon is credentialed OR the budget is
      // spent. Both are terminal: the effect stops polling either way.
      if (!awaiting || Date.now() - startedAt > MAX_WAIT_MS) {
        setProvisioning(false);
        return;
      }

      setProvisioning(true);
      timer = window.setTimeout(tick, POLL_INTERVAL_MS);
    };

    let timer = window.setTimeout(tick, 0);

    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, []);

  return provisioning;
}
