/**
 * The single way out of `/onboarding`.
 *
 * Onboarding used to have three exits using three different mechanisms: the
 * route's `useNavigate` for the already-complete redirect, the same for the
 * returning-user heal, and the imported global `router` singleton — called
 * un-awaited from inside an async helper — for the terminal steps.
 *
 * That asymmetry was the bug, not a detail of it. Because the terminal-step
 * exit lived outside the router the route is mounted under, it fired before
 * the step could render `DaemonConnectingGate`, dropping cloud users on `/`
 * with onboarding marked complete and no ACTIVE daemon. The fix at the time
 * threaded a `navigate: false` flag down to the singleton, which then had to
 * be passed correctly at four call sites, each re-deriving cloud-ness with its
 * own copy of the same string literal. One wrong call site reproduces the
 * original bug on one path.
 *
 * Funnelling every exit through here deletes that flag rather than testing it.
 * The cloud path cannot leave before the gate, because the gate's Continue is
 * the only cloud exit reason that exists.
 *
 * Every exit is logged and tracked with its reason. In the renderer that log
 * line lands in `frontend_reliant-web.log` prefixed `[browser:…]`, in Electron
 * and in a browser tab alike — so "something ended onboarding" stops being
 * invisible and becomes a grep. An exit with a reason nobody triggered is now
 * a line in a file rather than a user's confused bug report.
 */
import { logger } from "@/lib/logger";
import { trackEvent } from "@/lib/analytics";
import { ONBOARDING_STEPS as TOUR_STEPS } from "@/components/Onboarding/constants";
import { stepsViewedCount } from "./analytics";

/**
 * The first step of the post-onboarding guided tour.
 *
 * `?tour=<id>` is the ONLY thing that starts the tour (see useTourNavigation),
 * so an exit that drops it ends the tour before it renders.
 */
export const TOUR_FIRST_STEP_ID = TOUR_STEPS[0].id;

/**
 * Why the user is leaving. Exhaustive by construction — a new way out means a
 * new member here, which is the point: the set of exits is readable in one
 * place instead of being discovered by grepping for `navigate`.
 */
export const ONBOARDING_EXIT_REASONS = [
  /** A local-daemon user finished the final step. Nothing left to wait for. */
  "completed_local",
  /** A cloud user finished and then dismissed the daemon-connecting gate. */
  "completed_cloud_gate_continue",
  /** The user was already complete and should never have been on this route. */
  "already_complete",
  /** A returning user with a pre-existing daemon; the flag was repaired. */
  "returning_user_heal",
  /** The user signed out from the escape hatch. */
  "signed_out",
] as const;

export type OnboardingExitReason = (typeof ONBOARDING_EXIT_REASONS)[number];

/**
 * Search params to carry when leaving for the app WITHOUT having finished.
 *
 * These redirects used to pass `search: {}`, which erases everything —
 * including `?tour=`, so a redirect that raced the handoff silently ended the
 * tour. `tour` is the only param worth forwarding: the rest of onboarding's
 * search (`plan`, the `github_*` return params) is flow-local and must not
 * leak into the app, so this whitelists rather than spreading `prev`.
 */
const keepTourHandoff = (prev: Record<string, unknown>) =>
  prev.tour ? { tour: prev.tour } : {};

/** Where an exit lands the user. */
export interface OnboardingExitTarget {
  to: string;
  search:
    | Record<string, unknown>
    | ((prev: Record<string, unknown>) => Record<string, unknown>);
  replace?: boolean;
}

/**
 * The destination for each reason.
 *
 * The two `completed_*` reasons SET the tour param rather than preserving it:
 * a cloud user's finalize never touched the URL, so there is nothing in `prev`
 * to preserve, and relying on that is what dropped the tour for every cloud
 * user.
 */
export function onboardingExitTarget(
  reason: OnboardingExitReason,
): OnboardingExitTarget {
  switch (reason) {
    case "completed_local":
    case "completed_cloud_gate_continue":
      return { to: "/", search: { tour: TOUR_FIRST_STEP_ID } };
    case "already_complete":
    case "returning_user_heal":
      return { to: "/", search: keepTourHandoff };
    case "signed_out":
      // Explicit rather than left to AuthGuard: the guard does bounce an
      // unauthenticated user eventually, but there is a render gap in which
      // this route's own "needs onboarding" logic re-asserts itself and puts
      // the user straight back here.
      return { to: "/auth", search: { redirect: undefined } };
  }
}

/** Navigates. Satisfied by tanstack-router's `useNavigate` result. */
export type NavigateFn = (options: OnboardingExitTarget) => Promise<void> | void;

/**
 * Leave `/onboarding`, recording why.
 *
 * Takes the navigate function rather than importing the router, so the caller
 * always drives the router it is actually mounted under — the thing the old
 * singleton path got wrong.
 */
export async function leaveOnboarding(
  reason: OnboardingExitReason,
  navigate: NavigateFn,
): Promise<void> {
  const stepsViewed = stepsViewedCount();

  logger.info("[Onboarding] exit", { reason, steps_viewed_count: stepsViewed });
  trackEvent("onboarding_exited", {
    reason,
    steps_viewed_count: stepsViewed,
  });

  await navigate(onboardingExitTarget(reason));
}
