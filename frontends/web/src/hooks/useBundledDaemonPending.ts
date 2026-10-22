import { useEffect, useRef, useState } from "react";

/**
 * Whether the desktop app's bundled daemon is still on its way, so an empty
 * ListDaemons result should not yet be read as "this user has no daemon".
 *
 * ── The window this covers ────────────────────────────────────────────
 *
 * A packaged build ships its own daemon, but that daemon holds no credentials
 * until the user signs in. After sign-in the app restarts it with the new
 * principal, and only then does it register. Measured on a real prod sign-in:
 *
 *   02:20:10.3  daemon restarted with the new principal
 *   02:20:11.2  registered — NOW listable via ListDaemons
 *   02:20:12.4  gateway stream connected
 *
 * ListDaemons is legitimately empty across that window. Onboarding's compute
 * step reads an empty list as "no daemon" and asks the user to choose their
 * compute — a question that answers itself moments later, except the step
 * latches on interaction, so the user is stuck with the answer they gave.
 *
 * ── Why this waits on an EVENT, not a timer ───────────────────────────
 *
 * An earlier version of this hook ran a wall-clock budget and shipped
 * broken. Two reasons, both worth keeping written down:
 *
 *   - The renderer REMOUNTS after the post-sign-in daemon restart, which
 *     reset the budget and restarted the whole wait.
 *   - A budget only bounds the wait; it cannot end it early. The user still
 *     sat through the full timeout because nothing told the UI to look again
 *     — React Query's daemon poll sets `refetchIntervalInBackground: false`,
 *     and OAuth backgrounds the window by design.
 *
 * So the resolution here is `daemonFound` flipping true, which happens as
 * soon as the daemon-connected IPC event triggers a refetch (see
 * useDaemonStatus). The timeout below is only a floor against a daemon that
 * never arrives, not the mechanism.
 *
 * Returns false in a browser, where an empty list genuinely means "no
 * daemon", so web behaviour is unchanged.
 */

// Only a backstop. A daemon that never registers (crash, revoked token,
// offline) must not pin onboarding on a spinner — the user still gets the
// choice and can pick cloud instead. Generous relative to the ~2s observed
// end to end, because expiring EARLY reintroduces the original bug.
const MAX_WAIT_MS = 30_000;

const hasBundledDaemon = (): boolean =>
  typeof window !== "undefined" &&
  Boolean((window as unknown as { electronAPI?: unknown }).electronAPI);

export function useBundledDaemonPending(daemonFound: boolean): boolean {
  const [pending, setPending] = useState(hasBundledDaemon);
  // Survives remounts of this component, so the post-sign-in renderer reload
  // does not restart the wait. `useRef` alone would reset; the module-level
  // anchor below is what makes it stable across the remount.
  const startedAt = useRef(anchorStart());

  // A renderer that has been alive longer than the budget must not start out
  // already expired.
  //
  // The anchor is set on first use and never reset, so in a long-lived renderer
  // — every dev session with HMR, and any packaged app left open a while before
  // signing in — `remaining` is already negative when onboarding mounts. The
  // hook then returns false on the FIRST render, ComputeStep skips the
  // "Connecting your daemon…" state, and the user sees the compute CHOICE flash
  // up before the daemon registers and the auto-skip advances past it. That is
  // the question-that-answers-itself this hook exists to suppress.
  //
  // Re-anchoring when a wait actually begins keeps the budget meaningful: it
  // bounds THIS wait, not the age of the renderer. The remount-stability the
  // module anchor provides is preserved, because re-anchoring happens only when
  // no wait is in progress.
  if (pending && Date.now() - startedAt.current >= MAX_WAIT_MS) {
    startedAt.current = reanchorStart();
  }

  useEffect(() => {
    if (!hasBundledDaemon()) {
      setPending(false);
      return;
    }
    if (daemonFound) {
      setPending(false);
      return;
    }

    const remaining = MAX_WAIT_MS - (Date.now() - startedAt.current);
    if (remaining <= 0) {
      setPending(false);
      return;
    }

    const timer = window.setTimeout(() => setPending(false), remaining);
    return () => window.clearTimeout(timer);
  }, [daemonFound]);

  return pending;
}

/**
 * First time this hook was used in this renderer session.
 *
 * Module-scoped rather than per-component: the renderer RELOADS after the
 * post-sign-in daemon restart, and a per-component anchor restarts the budget
 * on that remount — which is exactly how the previous implementation ended up
 * making the user wait twice.
 */
let sessionStart: number | null = null;
function anchorStart(): number {
  if (sessionStart === null) sessionStart = Date.now();
  return sessionStart;
}

/**
 * Restart the budget for a new wait.
 *
 * Called only when the anchor has already fully expired while a wait is
 * beginning, which means the elapsed time belongs to the renderer's lifetime
 * rather than to this wait. Without it, a renderer older than MAX_WAIT_MS
 * starts every subsequent wait pre-expired — see the call site.
 */
function reanchorStart(): number {
  sessionStart = Date.now();
  return sessionStart;
}
