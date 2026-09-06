import { useEffect, useRef } from "react";

import { DaemonStatus as ControlPlaneDaemonStatus } from "@/gen/controlplane/v1/public/shared_pb";
import { hasUsableControlPlaneDaemonForOnboarding } from "./steps/ComputeStep";

/**
 * Repairs the onboarding flag for a user who already finished setting up but
 * was never marked complete.
 *
 * `onboarding_completed_at` is written only by the final project step, so a
 * user who set up a working daemon and closed the tab stays flagged
 * incomplete — and every landing on `/` bounces them back into onboarding
 * forever. Since compute now requires a coupon or a plan, that user can be
 * genuinely stuck: blocked at step 1, unable to reach the step that would mark
 * them complete, locked out of the app they already have a machine for.
 *
 * A usable daemon is what onboarding exists to produce, so its presence is
 * treated as proof the user already got there.
 *
 * ── The one thing this has to get right ───────────────────────────────
 *
 * "Has a daemon" is NOT the same question as "arrived with a daemon", and
 * healing on the former ends the flow the user is actively in. Onboarding
 * provisions a machine mid-flow (the compute step, and redeeming a compute
 * coupon), so a naive check sees that daemon and declares the user finished:
 *
 *     daemon created      19:07:33.253
 *     onboarding completed 19:07:33.274   ← 21ms later, no step in between
 *
 * Two things are therefore excluded:
 *
 *  1. Daemons that PREDATE the account. The desktop app's bundled daemon
 *     re-registers under whoever signs in, so "this user has a daemon" is true
 *     within a second of any first-ever sign-in.
 *  2. Daemons that appeared after we first saw this user mid-flow — this
 *     session's own work.
 *
 * Rule 2 is recorded in sessionStorage rather than a ref because the GitHub
 * OAuth round trip is a FULL PAGE NAVIGATION: the app unloads and every ref
 * resets, which is precisely when the flow's own daemon got mistaken for
 * evidence of a past completion. sessionStorage survives that navigation and
 * dies with the tab, which is the exact lifetime this fact should have.
 */

/**
 * Minimal shape this hook reads off a daemon; matches the control-plane Daemon.
 *
 * Both fields are load-bearing: `createdAt` dates the daemon against the
 * account, and `status` is what `hasUsableControlPlaneDaemonForOnboarding`
 * inspects. `status` is the control-plane enum specifically — the reliant
 * enum of the same name has different numeric values.
 */
type DatedDaemon = {
  createdAt?: { seconds: bigint };
  status: ControlPlaneDaemonStatus;
};

const IN_PROGRESS_KEY_PREFIX = "reliant:onboarding:in-progress:";

/**
 * Whether this user has been seen mid-onboarding with no usable daemon.
 *
 * Storage throws in Safari private mode, in sandboxed iframes, and under the
 * `app://bundle` origin the packaged Electron renderer runs on — an origin
 * whose storage partitioning has already caused one bug in this repo
 * (`3fcd9f79`).
 *
 * When it throws, the answer is UNKNOWN, not false. This distinction is the
 * whole defence: the mark is what stops onboarding's own freshly-provisioned
 * daemon being read as evidence of a past completion, and treating a failed
 * read as "not seen mid-flow" silently restores exactly that bug. So an
 * unavailable store reports `unknown` and the caller declines to heal —
 * ending a flow wrongly is unrecoverable for the user, while declining costs
 * them one extra pass through onboarding.
 */
type InProgressMark = "seen" | "not-seen" | "unknown";

const inProgress = {
  get(userId: string | undefined): InProgressMark {
    if (!userId) return "not-seen";
    try {
      return sessionStorage.getItem(IN_PROGRESS_KEY_PREFIX + userId) === "true"
        ? "seen"
        : "not-seen";
    } catch {
      return "unknown";
    }
  },
  /** Returns false when the mark could not be persisted. */
  set(userId: string | undefined): boolean {
    if (!userId) return false;
    try {
      sessionStorage.setItem(IN_PROGRESS_KEY_PREFIX + userId, "true");
      return true;
    } catch {
      return false;
    }
  },
};

export interface ReturningUserHealDeps<T extends DatedDaemon> {
  /** Whose in-progress mark to read and write. Undefined until loaded. */
  userId: string | undefined;
  /**
   * Account creation time, epoch ms. Undefined until the user query resolves,
   * which is also how this hook knows the user has loaded.
   */
  userCreatedAtMs: number | undefined;
  daemons: ReadonlyArray<T> | undefined;
  daemonsLoading: boolean;
  /** True once the server says onboarding is complete. */
  isComplete: boolean;
  /** Records the completion server-side. Fires at most once. */
  onHeal: () => void;
}

/**
 * Heals a stranded returning user, once per mount. A no-op for everyone else.
 */
export function useReturningUserHeal<T extends DatedDaemon>({
  userId,
  userCreatedAtMs,
  daemons,
  daemonsLoading,
  isComplete,
  onHeal,
}: ReturningUserHealDeps<T>): void {
  const healedRef = useRef(false);
  // Sticky: once storage has failed we can never again distinguish "this
  // user's daemon predates the flow" from "this flow just made it", and a
  // later successful read would be answering from an empty store rather than
  // from a genuine absence of the mark.
  const storageBrokenRef = useRef(false);

  // Both queries must have settled. They run in parallel and the daemon list
  // (a fast local call) routinely wins, so acting on it alone would judge the
  // user against an unknown account — which dates every daemon out and reads
  // as "no daemons" no matter what is really there.
  //
  // `userCreatedAtMs !== undefined` IS the user-loaded check: the field only
  // has a value once the query resolved. A separate `!userLoading` term looked
  // like a third guard but could never independently be false here.
  const ready = !daemonsLoading && userCreatedAtMs !== undefined;

  let arrivedWithDaemon = false;
  if (ready) {
    const ownDaemons = (daemons ?? []).filter((daemon) =>
      daemon.createdAt
        ? Number(daemon.createdAt.seconds) * 1000 >= userCreatedAtMs
        : false,
    );
    const hasDaemon = hasUsableControlPlaneDaemonForOnboarding(ownDaemons);

    if (hasDaemon) {
      // Proof of a PRIOR completion only if we have not already watched this
      // user start from nothing in this session — and only if we can still
      // trust the record of that.
      const mark = inProgress.get(userId);
      if (mark === "unknown") storageBrokenRef.current = true;
      arrivedWithDaemon = mark === "not-seen" && !storageBrokenRef.current;
    } else {
      // Mid-flow with no daemon yet. Remember it, so a later remount cannot
      // reinterpret the daemon this flow creates as a past completion. If the
      // write failed there is nothing to remember it WITH, so the heal must
      // stay off for the rest of this mount.
      if (!inProgress.set(userId)) storageBrokenRef.current = true;
    }
  }

  const shouldHeal = arrivedWithDaemon && !isComplete;

  useEffect(() => {
    if (!shouldHeal || healedRef.current) return;
    healedRef.current = true;
    onHeal();
  }, [shouldHeal, onHeal]);
}
