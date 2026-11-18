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
 * The gate is on by default for the `_authenticated` layout of BOTH shells —
 * Reliant's (routes.tsx) and the forge site's (forgeShellRoutes.tsx, which
 * mounts the same wizard at the same `/onboarding` route id) — so a new route
 * is covered without anyone remembering to add it. What is exempt, and why:
 *
 *   /onboarding    the wizard itself.
 *   /settings      reachable mid-onboarding by design: the escape hatch's
 *                  "Settings" (sign out, delete the account) and the billing
 *                  detour (`?from=onboarding`) both land there
 *                  (OnboardingEscapeHatch, Settings/cloud/billing).
 *   /m             (Reliant) MobileShell runs the same redirect for the phone
 *                  surface.
 *   /forge/demo    (Reliant) the forge journey's console on fixture data:
 *                  nothing of the user's, offered to everyone. The forge
 *                  site mounts it outside the guard.
 *   /apps          (Reliant) where a claim lands there (Claim/useClaimFlow).
 *   a claim        the tab that just claimed a site (markClaimLanding): the
 *                  claim design keeps a new claimant out of the wizard on the
 *                  way to their claimed app, and the wizard has no
 *                  claim-aware path. On the forge site a claim lands in the
 *                  app's console, so this is a mark on the tab, not a route.
 *                  The next visit is gated.
 *   a skip         (forge site) the wizard's own "Only here to deploy apps?"
 *                  escape (OnboardingEscapeHatch), remembered for this user
 *                  in this browser (markForgeSetupSkipped). It leaves the
 *                  server's onboarding unfinished, so Reliant still walks
 *                  them through it if they come to the agent side.
 *
 * Only a user the server says has NOT finished is redirected. A user whose
 * record failed to load is let through: a control-plane hiccup must not trap
 * someone on a spinner, and `/` still asks again.
 */

import { isSafeReturnTo } from "@/lib/returnTo";

/** Path prefixes a not-yet-onboarded user may open without the wizard, in Reliant. */
export const ONBOARDING_EXEMPT_PREFIXES: readonly string[] = ["/onboarding", "/settings", "/m", "/forge/demo", "/apps"];

/**
 * The same, on the forge site: `/apps` and the console are gated there (a
 * claim is exempted by its mark instead), and it has no `/m` or demo route
 * under its guard.
 */
export const FORGE_SHELL_ONBOARDING_EXEMPT_PREFIXES: readonly string[] = ["/onboarding", "/settings"];

const underPrefix = (pathname: string, prefix: string) => pathname === prefix || pathname.startsWith(`${prefix}/`);

/** Whether `pathname` (a route under the `_authenticated` layout) needs onboarding finished. */
export function routeRequiresOnboarding(
  pathname: string,
  exempt: readonly string[] = ONBOARDING_EXEMPT_PREFIXES,
): boolean {
  return !exempt.some((prefix) => underPrefix(pathname, prefix));
}

/** sessionStorage key of the claim mark: the claimant's auth user id. */
export const CLAIM_LANDING_KEY = "reliant.onboarding.claimLanding";

/** Record that this tab just claimed a site as `userId` (Claim/useClaimFlow). */
export function markClaimLanding(userId: string | undefined): void {
  if (!userId) return;
  try {
    sessionStorage.setItem(CLAIM_LANDING_KEY, userId);
  } catch {
    // Storage refused: the claimant is gated like anyone else.
  }
}

/** localStorage key of the forge site's skip: the auth user id that chose it. */
export const FORGE_SETUP_SKIP_KEY = "reliant.onboarding.forgeSkip";

/** Remember that `userId` chose to skip setup and use only the forge console. */
export function markForgeSetupSkipped(userId: string | undefined): void {
  if (!userId) return;
  try {
    localStorage.setItem(FORGE_SETUP_SKIP_KEY, userId);
  } catch {
    // Storage refused: the skip holds for this navigation only.
  }
}

/** Whether `userId` chose, in this browser, to skip setup on the forge site. */
export function forgeSetupSkippedBy(userId: string | undefined): boolean {
  if (!userId) return false;
  try {
    return localStorage.getItem(FORGE_SETUP_SKIP_KEY) === userId;
  } catch {
    return false;
  }
}

/** Whether this tab claimed a site as `userId`. */
export function claimLandedAs(userId: string | undefined): boolean {
  if (!userId) return false;
  try {
    return sessionStorage.getItem(CLAIM_LANDING_KEY) === userId;
  } catch {
    return false;
  }
}

export type OnboardingGateDecision = "allow" | "wait" | "onboard";

/** What the gate does on this route for this user. Pure. */
export function onboardingGateDecision(state: {
  pathname: string;
  userLoading: boolean;
  user: { onboardingCompleted: boolean } | null | undefined;
  /** Exempt path prefixes (default: Reliant's). */
  exempt?: readonly string[];
  /** The user is excused this visit: a claim landing, or the forge site's skip. */
  excused?: boolean;
}): OnboardingGateDecision {
  if (!routeRequiresOnboarding(state.pathname, state.exempt)) return "allow";
  if (state.excused) return "allow";
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
