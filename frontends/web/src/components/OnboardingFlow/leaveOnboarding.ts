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
import { TOUR_PICKER } from "@/components/Onboarding/tourParam";
import { startForIntent } from "@/components/Apps/appStart";
import { stepsViewedCount } from "./analytics";
import { isOnboardingReturnTo } from "./onboardingGate";
import type { OnboardingIntent } from "./types";

/**
 * The journey picker's `?tour=` value. Finishing onboarding no longer opens it
 * (see onboardingExitTarget); it stays exported for the places that still do —
 * the home view's "quick tour" link and Settings → About.
 */
export const TOUR_ENTRY_PARAM = TOUR_PICKER;

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
 * What the user said they came to do, for the exits that finish the flow.
 * It picks the first message the new chat offers (appStart.ts): "build_app" the
 * app-building composer, "existing_codebase" a deploy of their repository,
 * anything else a first question about the project.
 */
export type OnboardingLanding = OnboardingIntent | undefined;

/**
 * The destination for each reason.
 *
 * The two `completed_*` reasons land on a NEW CHAT with a first message ready
 * in the composer, and NOT on the journey picker. In the first week none of
 * the 12 external signups sent a chat: the two who finished onboarding were
 * dropped on a seven-step tour or the forge journey, and neither came back to
 * the composer. The tour is still reachable (the heading's "quick tour" link,
 * Settings → About, the setup checklist); it is just no longer the first thing
 * between a new user and the product.
 *
 * `tour: undefined` is explicit: the root route retains `?tour=` across
 * navigations that do not name it (routes.tsx), and an exit has to say it is
 * clearing it.
 */
export function onboardingExitTarget(
  reason: OnboardingExitReason,
  landing?: OnboardingLanding,
): OnboardingExitTarget {
  switch (reason) {
    case "completed_local":
    case "completed_cloud_gate_continue":
      return { to: "/", search: { start: startForIntent(landing), tour: undefined } };
    case "already_complete":
    case "returning_user_heal":
      return { to: "/", search: keepTourHandoff };
    case "signed_out":
      // Explicit rather than left to AuthGuard: the guard does bounce an
      // unauthenticated user eventually, but there is a render gap in which
      // this route's own "needs onboarding" logic re-asserts itself and puts
      // the user straight back here.
      // `tour: undefined`: the root route otherwise carries `?tour=` along.
      return { to: "/auth", search: { redirect: undefined, tour: undefined } };
  }
}

/** An exit back to the page the user was going to (`returnTo`), by href. */
export interface OnboardingReturnTarget {
  href: string;
  replace?: boolean;
}

/** Every reason that leaves for the app, as opposed to signing out. */
const RETURNS_TO_APP: ReadonlySet<OnboardingExitReason> = new Set([
  "completed_local",
  "completed_cloud_gate_continue",
  "already_complete",
  "returning_user_heal",
]);

/**
 * Where an exit actually goes: back to `returnTo` when OnboardingGate sent the
 * user here from a page they asked for (a deep link into the forge console,
 * say) and the reason leaves for the app; otherwise the reason's destination
 * (onboardingExitTarget). The page they asked for wins over the new-chat
 * landing: they came for it, and the composer is one click away from there.
 * `returnTo` comes off the address bar, so it is re-checked here
 * (isOnboardingReturnTo: same-origin, never /onboarding or /auth).
 */
export function onboardingExitNavigation(
  reason: OnboardingExitReason,
  landing?: OnboardingLanding,
  returnTo?: string,
): OnboardingExitTarget | OnboardingReturnTarget {
  if (RETURNS_TO_APP.has(reason) && isOnboardingReturnTo(returnTo)) {
    return { href: returnTo, replace: true };
  }
  return onboardingExitTarget(reason, landing);
}

/** Navigates. Satisfied by tanstack-router's `useNavigate` result. */
export type NavigateFn = (options: OnboardingExitTarget | OnboardingReturnTarget) => Promise<void> | void;

/**
 * Leave `/onboarding`, recording why.
 *
 * Takes the navigate function rather than importing the router, so the caller
 * always drives the router it is actually mounted under — the thing the old
 * singleton path got wrong. `returnTo` is the route's `returnTo` search param
 * (OnboardingGate), when there is one.
 */
export async function leaveOnboarding(
  reason: OnboardingExitReason,
  navigate: NavigateFn,
  landing?: OnboardingLanding,
  returnTo?: string,
): Promise<void> {
  const stepsViewed = stepsViewedCount();
  const target = onboardingExitNavigation(reason, landing, returnTo);

  logger.info("[Onboarding] exit", {
    reason,
    landing,
    steps_viewed_count: stepsViewed,
    ...("href" in target ? { return_to: target.href } : {}),
  });
  trackEvent("onboarding_exited", {
    reason,
    steps_viewed_count: stepsViewed,
    returned_to_deep_link: "href" in target,
  });

  await navigate(target);
}
