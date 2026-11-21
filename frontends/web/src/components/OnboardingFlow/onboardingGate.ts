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
 *
 * The homes are the exception (homeTarget, below): a person who cannot run a
 * chat yet — not onboarded, or onboarded with no usable machine — lands on
 * their projects instead, with a "Set up chats" card (SetUpChatsCard), and is
 * walked through a machine only when they go for a chat.
 */

import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { isSafeReturnTo } from "@/lib/returnTo";
import { appsPath, onboardingPath, projectsPath, settingsPath, withSearch } from "@/lib/paths";
import { orgFreePath } from "@/lib/paths/org";

import { hasUsableDaemonForOnboarding } from "./usableDaemon";

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
 * chat route: to onboarding, remembering `href` — or, at a home, to their
 * projects (homeTarget).
 */
export function onboardingGateTarget(href: string, forgeBrand = false) {
  const home = homeTarget(href.split(/[?#]/, 1)[0], "onboard", { forgeBrand });
  if (home) return { to: home, search: {}, replace: true };
  return {
    to: onboardingPath(),
    search: isOnboardingReturnTo(href) ? { returnTo: href } : {},
    replace: true,
  };
}

// ─── The daemonless home (U6) ───────────────────────────────────────────────
//
// Owner, 2026-11-19: don't push people through machine setup up front. A
// person who cannot run a chat yet lands on their projects (their apps, and
// a "Set up chats" card); the chat surfaces walk them through connecting a
// machine when they go for one. Both brands, one rule.

/**
 * What chats need from this person before they can run, as far as the app
 * knows right now:
 *
 *   checking         the user record, or (once onboarded) the daemon list,
 *                    has not answered yet
 *   unknown          one of them failed to load: act as if nothing is
 *                    missing, so a hiccup never moves anyone
 *   ready            onboarded, with a usable machine
 *                    (hasUsableDaemonForOnboarding)
 *   onboard          onboarding not finished: the wizard sets up a machine
 *   connect-machine  onboarded, and the daemon list settled with no usable
 *                    machine in it (they skipped it, or removed it)
 */
export type ChatSetup = "checking" | "unknown" | "ready" | "onboard" | "connect-machine";

/** Whether `setup` is one the person has to act on before a chat can run. */
export function chatSetupNeeded(setup: ChatSetup): setup is "onboard" | "connect-machine" {
  return setup === "onboard" || setup === "connect-machine";
}

/** What chats need from this person (ChatSetup). Pure. */
export function chatSetup(facts: {
  userLoading: boolean;
  user: { onboardingCompleted: boolean } | null | undefined;
  /** True while the daemon list has no answer yet (useDaemonList's first load). */
  daemonsLoading: boolean;
  /** The daemon list's last answer; undefined when it has none (loading, or the first read failed). */
  daemons: ReadonlyArray<Pick<DaemonInfo, "status">> | undefined;
}): ChatSetup {
  if (facts.userLoading) return "checking";
  if (!facts.user) return "unknown";
  if (!facts.user.onboardingCompleted) return "onboard";
  // A refetch that fails keeps the last answer, so a settled list does not
  // flap on a transient error; only a list that never answered is unknown.
  if (facts.daemons) return hasUsableDaemonForOnboarding(facts.daemons) ? "ready" : "connect-machine";
  return facts.daemonsLoading ? "checking" : "unknown";
}

/**
 * Where "Set up chats" (and New chat, while chats cannot run) takes a person:
 * the wizard if they have not onboarded — it is what connects a first machine,
 * and it comes back to `returnTo` when it is done — else Settings › Machines,
 * where the one Add machine sheet lives (the wizard leaves at once for anyone
 * already onboarded: OnboardingRoute, useArrivedComplete).
 */
export function chatSetupPath(setup: "onboard" | "connect-machine", returnTo?: string): string {
  if (setup === "connect-machine") return settingsPath("machines");
  return isOnboardingReturnTo(returnTo) ? withSearch(onboardingPath(), { returnTo }) : onboardingPath();
}

/** The desktop home, `/` (and `/o/<org>`): the new-chat view. */
const DESKTOP_HOME = "/";
/** The new-chat view by its own URL (U5): `/new` (and `/o/<org>/new`). */
const NEW_CHAT = "/new";
/** The phone's homes: its chat list. */
const MOBILE_HOMES = new Set(["/m", "/m/chats"]);

/**
 * Where a home sends this person instead, or null to stay.
 *
 * At the desktop home (`/`, `/o/<org>`), a person who cannot run a chat yet
 * (chatSetupNeeded) goes to their projects, on both brands — unless the URL
 * asked for a chat (`chatIntent`: an onboarding plan, a starter in the
 * composer), which onboards them as a chat route does. Checking or unknown
 * stays put: no redirect on a guess, so the home never flaps.
 *
 * The phone keeps its earlier rule: on the forge brand its chat list sends a
 * person who has not onboarded to their apps; otherwise the chat routes'
 * onboarding redirect applies (MobileShell).
 */
export function homeTarget(
  pathname: string,
  setup: ChatSetup,
  options: { forgeBrand: boolean; chatIntent?: boolean },
): string | null {
  const path = orgFreePath(pathname).replace(/\/+$/, "") || "/";
  if (path === DESKTOP_HOME) return chatSetupNeeded(setup) && !options.chatIntent ? projectsPath() : null;
  if (MOBILE_HOMES.has(path)) return options.forgeBrand && setup === "onboard" ? appsPath() : null;
  return null;
}

/** What a chat route (ModernApp's `/`, `/project/$projectId`, `/c/…` and `/new`) does for this person. */
export type ChatRouteAction =
  | { kind: "stay" }
  /** Hold the page (a spinner) without redirecting yet. */
  | { kind: "wait" }
  /** Leave for `to`; `search` "keep" carries the URL's own search along. */
  | { kind: "go"; to: string; search: Record<string, unknown> | "keep" };

/**
 * The one decision behind ModernApp's redirect (useChatRouteRedirect). Pure.
 *
 *   - A home sends a person who cannot chat yet to their projects
 *     (homeTarget), and waits while that is still being checked.
 *   - A chat route sends a person who has not onboarded (or whose record
 *     failed to load, which the wizard asks again) to `/onboarding`,
 *     remembering where they were going; a home with a chat intent carries
 *     its search (`?plan=`, `?start=`) along instead, as it always did.
 *   - An onboarded person with no machine who opens the new-chat view
 *     (`/new`), or a home that asked for a chat (`?plan=`, `?start=`,
 *     `?reset-onboarding`), goes where New chat takes them in that state
 *     (chatSetupPath: Settings › Machines), and it waits while that is still
 *     being checked.
 *   - An onboarded person with no machine who opens a project's chats, or a
 *     chat by its URL (`/c/…`), stays: the chat has its own Connect a machine
 *     (Chat/NoMachine.tsx).
 */
export function chatRouteAction(state: {
  pathname: string;
  /** The whole URL (path, search, hash), for the wizard's returnTo. */
  href: string;
  /** Whether the URL asks for a chat: an onboarding plan, a starter (homeTarget). */
  chatIntent: boolean;
  userLoading: boolean;
  user: { onboardingCompleted: boolean } | null | undefined;
  setup: ChatSetup;
  forgeBrand: boolean;
}): ChatRouteAction {
  if (!routeRequiresOnboarding(state.pathname)) return { kind: "stay" };
  if (state.userLoading) return { kind: "wait" };
  const home = homeTarget(state.pathname, state.setup, { forgeBrand: state.forgeBrand, chatIntent: state.chatIntent });
  if (home) return { kind: "go", to: home, search: {} };
  const path = orgFreePath(state.pathname).replace(/\/+$/, "") || "/";
  const atHome = path === DESKTOP_HOME;
  const atNewChat = path === NEW_CHAT;
  if (!state.user || !state.user.onboardingCompleted) {
    if (atHome) return { kind: "go", to: onboardingPath(), search: "keep" };
    const target = onboardingGateTarget(state.href);
    return { kind: "go", to: target.to, search: target.search };
  }
  // The new-chat view, and a home that asked for a chat (it shows the
  // new-chat view with the plan or starter), are New chat: with no machine
  // they go where New chat goes in that state.
  const newChat = atNewChat || (atHome && state.chatIntent);
  if (newChat && state.setup === "connect-machine") return { kind: "go", to: chatSetupPath(state.setup), search: {} };
  // The home and the new-chat view are about to learn whether there is a
  // machine: hold them rather than show the new-chat view for a frame and
  // then leave.
  if ((atHome || atNewChat) && state.setup === "checking") return { kind: "wait" };
  return { kind: "stay" };
}
