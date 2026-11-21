/**
 * Which routes send a user who has not finished onboarding through
 * `/onboarding` first, and how they come back to the page they asked for.
 *
 * Onboarding sets up what CHATS need — a machine, a provider, a project — so
 * it guards the chat routes and nothing else (U6, control-plane
 * docs/design/unified-shell-and-projects.md). A person with no machine still
 * gets a working app everywhere else: their apps and each app's console,
 * Sites, Usage, Workflows, the Inbox, Settings. Both hosts run this one rule
 * (the forge host mounts the same route tree, routes.tsx).
 *
 * The chat routes, read through orgFreePath so `/o/<org>/…` matches as well:
 *
 *   /                  the chat home (ModernApp)
 *   /project/<id>      one project's chats (ModernApp)
 *   /c/<chatId>[/t/<threadId>]
 *                      a chat by its ID (ModernApp, U5)
 *   /new               a new chat (ModernApp, U5)
 *   /m                 the phone's chat list, and every chat under it
 *   /m/chats/…           (MobileShell)
 *   /m/new             a new chat on the phone (MobileShell)
 *
 * Three places act on it, all asking routeRequiresOnboarding: OnboardingGate
 * on the `_authenticated` layout (the phone's chat routes sit under it),
 * ModernApp (`/`, `/project/$projectId`, `/c/…`, `/new`, under `_app`) and MobileShell
 * (`/m/*`). Everything else is let through, including the wizard itself.
 *
 * Only a user the server says has NOT finished is redirected by the gate. A
 * user whose record failed to load is let through: a control-plane hiccup
 * must not trap someone on a spinner. (ModernApp and MobileShell also send a
 * user whose record failed to load to `/onboarding`, which asks again.)
 */

import { isSafeReturnTo } from "@/lib/returnTo";
import { appsPath, onboardingPath } from "@/lib/paths";
import { orgFreePath } from "@/lib/paths/org";

/** The org-less path prefixes of the chat routes, besides the home `/`: the only routes onboarding guards. */
export const ONBOARDING_GATED_PREFIXES = ["/project", "/c", "/new", "/m/chats", "/m/new"] as const;

const underPrefix = (pathname: string, prefix: string) => pathname === prefix || pathname.startsWith(`${prefix}/`);

/** Whether `pathname` (either form, `/o/<org>/…` or org-less) is a chat route, which needs onboarding finished. */
export function routeRequiresOnboarding(pathname: string): boolean {
  const path = orgFreePath(pathname).replace(/\/+$/, "") || "/";
  // `/m` itself is the phone's chat list (a redirect to /m/chats).
  if (path === "/" || path === "/m") return true;
  return ONBOARDING_GATED_PREFIXES.some((prefix) => underPrefix(path, prefix));
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
  const pathname = orgFreePath(returnTo.split(/[?#]/, 1)[0]);
  return pathname !== "/" && !underPrefix(pathname, "/onboarding") && !underPrefix(pathname, "/auth");
}

/**
 * The navigation that sends a not-yet-onboarded user at `href` away from a
 * chat route: to onboarding, remembering `href` — or, at the home on the forge
 * brand, to their apps (forgeBrandHomeTarget).
 */
export function onboardingGateTarget(href: string, forgeBrand = false) {
  const home = forgeBrandHomeTarget(href.split(/[?#]/, 1)[0], forgeBrand);
  if (home) return { to: home, search: {}, replace: true };
  return {
    to: onboardingPath(),
    search: isOnboardingReturnTo(href) ? { returnTo: href } : {},
    replace: true,
  };
}

/** The homes a person lands on without asking for a chat: `/` (and `/o/<org>`), and the phone's chat list. */
const HOMES = new Set(["/", "/m", "/m/chats"]);

/**
 * Where a home (HOMES) sends a person who has not finished onboarding, on the
 * forge brand (lib/shell.ts), or null to onboard them as Reliant does. A
 * forge signup came to deploy, not to chat: until the daemonless home lands
 * (U6 PR3: Projects with a "Set up chats" card) their home is their apps, as
 * the forge host's `/` always was. Any other chat route (a project's chats, a
 * chat on the phone) still onboards them: they asked for a chat.
 */
export function forgeBrandHomeTarget(pathname: string, forgeBrand: boolean): string | null {
  if (!forgeBrand) return null;
  const path = orgFreePath(pathname).replace(/\/+$/, "") || "/";
  return HOMES.has(path) ? appsPath() : null;
}
