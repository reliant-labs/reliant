/**
 * Which Reliant routes send a user who has not finished onboarding through
 * `/onboarding` first, and how they come back to the page they asked for.
 *
 * The redirect used to live only in ModernApp (`/` and `/project/$projectId`)
 * and MobileShell (`/m/*`). Every other authenticated route was gated on
 * sign-in alone, so a deep link skipped the wizard: a user who came from
 * forge — "Open in Reliant" on forge.reliantlabs.io lands on
 * `/forge/p/<project>/…` — reached the forge console, the inbox or workflows
 * with no machine and no project, and found the dead ends there instead.
 *
 * The gate is on by default for the `_authenticated` layout, so a new route is
 * covered without anyone remembering to add it. What is exempt, and why:
 *
 *   /onboarding    the wizard itself.
 *   /settings      reachable mid-onboarding by design: the escape hatch's
 *                  "Settings" (sign out, delete the account) and the billing
 *                  detour (`?from=onboarding`) both land there
 *                  (OnboardingEscapeHatch, Settings/cloud/billing).
 *   /m             MobileShell runs the same redirect for the phone surface.
 *   /forge/demo    the forge journey's console on fixture data: nothing of
 *                  the user's, offered to everyone.
 *   /apps          where a claim lands (Claim/useClaimFlow): the claim design
 *                  keeps a new claimant out of the wizard on the way to their
 *                  claimed app. Opening that app's console is gated.
 *
 * Only a user the server says has NOT finished is redirected. A user whose
 * record failed to load is let through: a control-plane hiccup must not trap
 * someone on a spinner, and `/` still asks again.
 */

import { isSafeReturnTo } from "@/lib/returnTo";

/** Path prefixes a not-yet-onboarded user may open without the wizard. */
export const ONBOARDING_EXEMPT_PREFIXES = ["/onboarding", "/settings", "/m", "/forge/demo", "/apps"] as const;

const underPrefix = (pathname: string, prefix: string) => pathname === prefix || pathname.startsWith(`${prefix}/`);

/** Whether `pathname` (a route under the `_authenticated` layout) needs onboarding finished. */
export function routeRequiresOnboarding(pathname: string): boolean {
  return !ONBOARDING_EXEMPT_PREFIXES.some((prefix) => underPrefix(pathname, prefix));
}

export type OnboardingGateDecision = "allow" | "wait" | "onboard";

/** What the gate does on this route for this user. Pure. */
export function onboardingGateDecision(state: {
  pathname: string;
  userLoading: boolean;
  user: { onboardingCompleted: boolean } | null | undefined;
}): OnboardingGateDecision {
  if (!routeRequiresOnboarding(state.pathname)) return "allow";
  if (state.userLoading) return "wait";
  if (state.user && !state.user.onboardingCompleted) return "onboard";
  return "allow";
}

/**
 * Whether `returnTo` may be where onboarding sends the user when it is done: a
 * same-origin path (isSafeReturnTo) that is not the wizard or the sign-in
 * screen, either of which would loop.
 */
export function isOnboardingReturnTo(returnTo: string | undefined | null): returnTo is string {
  if (!isSafeReturnTo(returnTo)) return false;
  const pathname = returnTo.split(/[?#]/, 1)[0];
  return pathname !== "/" && !underPrefix(pathname, "/onboarding") && !underPrefix(pathname, "/auth");
}

/** The navigation that sends a user at `href` to onboarding, remembering `href`. */
export function onboardingGateTarget(href: string) {
  return {
    to: "/onboarding" as const,
    search: isOnboardingReturnTo(href) ? { returnTo: href } : {},
    replace: true,
  };
}
