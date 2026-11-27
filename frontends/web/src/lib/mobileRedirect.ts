/**
 * Deciding when a browser should be sent to the mobile surface.
 *
 * The `/m/*` routes existed but nothing ever navigated to them, so every phone
 * loaded the full desktop ADE — resizable sidebars, file tree, terminal tabs,
 * and a guided tour spotlighting chrome that does not fit — at 390px wide.
 * Everything the mobile surface does was unreachable unless you typed the URL
 * by hand.
 *
 * ## Why width and not a user-agent string
 *
 * User-agent sniffing is a maintenance treadmill and gets iPads and desktop
 * browsers in narrow windows wrong in both directions. What actually matters
 * is whether the desktop layout *fits*, and that is a width question.
 *
 * The coarse-pointer check is the second half: a 700px desktop browser window
 * has a mouse, so the desktop layout is still usable and its resize handles
 * still work. A phone has neither. Requiring both signals means we only
 * redirect devices that are genuinely phone-shaped.
 *
 * ## Why this is an escapable default, not a lock
 *
 * `?desktop=1` opts out and is remembered, because "show me the real app" is a
 * legitimate request — a tablet user, or someone debugging the desktop layout
 * on a small screen. A redirect with no exit is worse than no redirect.
 */

import { defaultParseSearch, defaultStringifySearch } from "@tanstack/react-router";

import {
  encodedSegment,
  mobileAutomationPath,
  mobileChatPath,
  mobileChatsPath,
  mobileInboxPath,
  mobileMachinesPath,
  mobileNewChatPath,
  mobileRunsPath,
  mobileSettingsPath,
  mobileWorkflowPath,
  mobileWorkflowsPath,
} from "./paths";
import { orgFreePath, orgOfPath } from "./paths/org";
import { orgSegment, type RouteOrgScope } from "./routeOrg";
import { isSafeReturnTo } from "./returnTo";
import { DESKTOP_OPT_OUT_KEY, hasCoarsePointer, PHONE_MAX_WIDTH, readDesktopOptOut } from "./phoneShape";

// What a phone is, and the desktop opt-out, live in lib/phoneShape (the root
// font size reads them too); re-exported here for the redirect's callers.
export { DESKTOP_OPT_OUT_PARAM, isPhoneViewport, readDesktopOptOut } from "./phoneShape";

/** Below this width the desktop shell's sidebars and panels stop fitting. */
export const MOBILE_MAX_WIDTH = PHONE_MAX_WIDTH;

export interface MobileRedirectEnv {
  pathname: string;
  search: string;
  width: number;
  /** True when the primary input is touch-like (no hover, coarse pointer). */
  coarsePointer: boolean;
  /** Whether the user previously opted out of the mobile surface. */
  optedOutOfMobile: boolean;
}

/**
 * Whether this load should be redirected to the mobile surface.
 *
 * Pure so the decision is testable without a DOM — the browser-facing wrapper
 * below supplies the environment.
 */
export function shouldRedirectToMobile(env: MobileRedirectEnv): boolean {
  const { width, coarsePointer, optedOutOfMobile } = env;
  // An org page (`/o/<org>/…`) is matched as its org-less twin.
  const pathname = orgFreePath(env.pathname);

  if (optedOutOfMobile) return false;

  // Already there.
  if (pathname === "/m" || pathname.startsWith("/m/")) return false;

  // Routes that must never be hijacked. Auth and OAuth callbacks are steps in
  // someone else's flow and carry state in the URL that a redirect would drop;
  // /onboarding is the shared setup flow which already reflows and is a
  // prerequisite for the mobile surface having anything to show.
  //
  // /forge is the Apps console (environments, releases, observability). It
  // has no `/m/*` twin on purpose: its chrome collapses to an off-canvas nav
  // below `md` (forge-ui/sidebar_layout.tsx), so the one console serves both,
  // and redirecting it would make Apps and Observability unreachable from a
  // phone — which is what this list used to do. /projects, the list of apps
  // the console opens from (the drawer's Apps), is one responsive page for the
  // same reason, and so were the retired /apps and /sites it replaced.
  const PRESERVED = [
    "/auth",
    "/oauth",
    "/connections/oauth",
    "/onboarding",
    "/reset-password",
    "/verify-email",
    "/upgrade",
    // A claim link is opened from wherever the agent's human reads their
    // messages, often a phone, and its page is built to fit one.
    "/claim",
    // An org invitation is opened from an email, often on a phone; its page
    // fits one, and its token was already moved into storage at boot.
    "/accept-invite",
    "/design-sandbox",
    "/forge",
    "/projects",
    "/apps",
    "/sites",
    // Rows of the one nav (U6e) with no phone twin: Connectors and the org's
    // Usage & billing are pages of the shell, which fits a phone, so the nav's
    // drawer opens them rather than the chat list.
    "/connectors",
    "/usage",
    // GTM (components/Gtm) has no phone twin: its pages are responsive pages
    // of the shell, a sheet for each dialog, so a phone stays on them.
    "/gtm",
  ];
  if (PRESERVED.some((p) => pathname === p || pathname.startsWith(`${p}/`))) {
    return false;
  }

  return width <= MOBILE_MAX_WIDTH && coarsePointer;
}

/**
 * Desktop settings sections that have a screen in /m/settings, by the id of
 * that screen (`/m/settings?section=<id>`). A section with no phone screen
 * lands on the list.
 */
export const MOBILE_SETTINGS_SECTION_FOR: Readonly<Record<string, string>> = {
  account: "account",
  billing: "billing",
  "git-connections": "github",
  ai: "ai",
  appearance: "appearance",
  notifications: "notifications",
  branches: "workspace",
  privacy: "privacy",
  about: "about",
};

/**
 * Where a redirected desktop path lands on the mobile surface.
 *
 * A redirect that always went to the chat list threw away what the link was
 * about: an Inbox notification, a run link a teammate pasted, a "go to
 * settings" prompt all became the chat list. Each desktop path with a mobile
 * twin maps to it; everything else still lands on the chat list, the mobile
 * home. A run IS a chat, so a run link opens that chat.
 *
 * The desktop paths are MATCHED here as literals (the inbound side); every
 * destination is built by lib/paths (the outbound side), an id captured from
 * the desktop path carried over byte for byte (encodedSegment), and a
 * settings section's query in URLSearchParams' bytes, as before.
 *
 * The shell's scope goes along (the URL is the only view state): the phone
 * reads the same `?projects=` and `?focus=` the desktop does
 * (lib/urlState/shellScope.ts), so every twin keeps them. A project's chat
 * home (`/project/<id>`, where the project switcher lands) is the phone's
 * chat list scoped to it, and a draft's project, machine and workspace
 * (`/new?project=&machine=&ws=`) are the phone's draft's.
 */
export function mobilePathFor(pathname: string, search = ""): string {
  // The mobile tree is not under the org yet (U15): an org page maps as its org-less twin.
  const path = orgFreePath(pathname).replace(/\/+$/, "") || "/";
  const query = new URLSearchParams(search);
  // Router-generated URLs quote JSON-looking strings (e.g. an id "123").
  // Decode those quotes before carrying the values, while accepting raw links too.
  const parsed = defaultParseSearch(search);
  for (const [key, value] of Object.entries(parsed)) {
    if (typeof value === "string") query.set(key, value);
  }

  // The path names the one project, which ends any scope carried with it.
  const home = PROJECT_HOME.exec(path);
  if (home) return `${mobileChatsPath()}${defaultStringifySearch({ projects: decodeSegment(home[1]) })}`;

  const twin = new URL(mobileTwinOf(path, query), "http://twin");
  if (path === "/new") {
    for (const [from, to] of NEW_CHAT_PARAMS) {
      const value = query.get(from);
      if (value) twin.searchParams.set(to, value);
    }
  }
  for (const key of PROJECTS_CARRIED) {
    const value = query.get(key);
    if (value && !twin.searchParams.has(key)) twin.searchParams.set(key, value);
  }
  // Match the router's serializer: ordinary strings stay readable, but
  // JSON-looking ids must stay strings when the destination parses them.
  return `${twin.pathname}${defaultStringifySearch(Object.fromEntries(twin.searchParams))}`;
}

/** The shell's scope params every page carries (routeSchemas PROJECTS_CARRIED). */
const PROJECTS_CARRIED = ["projects", "focus"] as const;

/** The chat home of one project (org-free, no trailing slash). */
const PROJECT_HOME = /^\/project\/([^/]+)$/;

/** `/new`'s params, as `/m/new` names them. */
const NEW_CHAT_PARAMS = [
  ["project", "project"],
  ["machine", "machine"],
  ["ws", "worktreeId"],
] as const;

function decodeSegment(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/** The mobile twin of an org-free desktop path, before the scope is carried. */
function mobileTwinOf(path: string, query: URLSearchParams): string {
  if (path === "/inbox" || path.startsWith("/inbox/")) return mobileInboxPath();

  // A chat (a thread of it opens as the chat: the phone picks threads in its own sheet), and a draft.
  const chat = /^\/c\/([^/]+)/.exec(path);
  if (chat) return mobileChatPath(encodedSegment(chat[1]));
  if (path === "/new") return mobileNewChatPath();

  const run = /^\/workflows\/runs\/([^/]+)/.exec(path);
  if (run) return mobileChatPath(encodedSegment(run[1]));
  if (path === "/workflows/runs") return mobileRunsPath();
  // A workflow's page (where its triggers are — what an automation link
  // opens) is its read-only viewer; an automation id is resolved first.
  const workflow = /^\/workflows\/library\/([^/]+)/.exec(path);
  if (workflow) return mobileWorkflowPath(encodedSegment(workflow[1]));
  const automation = /^\/(?:workflows\/)?automations\/([^/]+)/.exec(path);
  if (automation) return mobileAutomationPath(encodedSegment(automation[1]));
  if (path === "/workflows" || path.startsWith("/workflows/")) return mobileWorkflowsPath();

  if (path.startsWith("/settings/machines") || path.startsWith("/settings/environments")) return mobileMachinesPath();
  // Billing on a phone is a screen inside /m/settings. A visit that has a way
  // back (the claim page sends people here to subscribe) keeps it: the mobile
  // twin opens on billing and offers the same return. The App hosting tab
  // (`?tab=hosting`: "Set up hosting billing", a deploy queued on billing, a
  // hosting checkout's return) keeps its tab, so the screen opens on the
  // hosting card, and its `org`, which that card asks about before acting in
  // another organization. A checkout's `checkout=` marker is not carried: it
  // is for the desktop app's checkout window, and the card reads the server.
  if (path === "/settings/billing") {
    const params = new URLSearchParams({ section: "billing" });
    const returnTo = query.get("returnTo");
    if (isSafeReturnTo(returnTo)) params.set("returnTo", returnTo);
    if (query.get("tab") === "hosting") {
      params.set("tab", "hosting");
      const org = query.get("org");
      if (org) params.set("org", org);
    }
    return `${mobileSettingsPath()}?${params}`;
  }
  const section = /^\/settings\/([^/]+)$/.exec(path)?.[1];
  const mobileSection = section ? MOBILE_SETTINGS_SECTION_FOR[section] : undefined;
  if (mobileSection) return `${mobileSettingsPath()}?${new URLSearchParams({ section: mobileSection })}`;
  if (path === "/settings" || path.startsWith("/settings/")) return mobileSettingsPath();

  return mobileChatsPath();
}

/**
 * Whether the phone hand-off may run for `pathname` now. Off the org tree,
 * always. On an org page (`/o/<org>/…`) only once that org's page has
 * committed for a member (`routeOrgScope`, lib/routeOrg.ts): the commit makes
 * it the tab's org, which the mobile tree reads until it moves under the org
 * (U15), so the phone opens in the same org. Never from the "no access" page
 * or while the org is pending: the hand-off would open the previous org.
 */
export function mobileHandoffAllowed({
  pathname,
  committedPathname,
  routeOrgScope,
}: {
  pathname: string;
  committedPathname: string | null;
  routeOrgScope: RouteOrgScope;
}): boolean {
  const org = orgOfPath(pathname);
  if (org === null) return true;
  return (
    routeOrgScope.kind === "member" &&
    orgSegment(routeOrgScope.org) === org &&
    committedPathname !== null &&
    orgOfPath(committedPathname) === org
  );
}

/** Clears the opt-out so the browser returns to the mobile surface. */
export function clearDesktopOptOut(): void {
  try {
    localStorage.removeItem(DESKTOP_OPT_OUT_KEY);
  } catch {
    // Nothing to clear if storage is unavailable.
  }
}

/**
 * Browser-facing wrapper. Returns false in non-DOM contexts (tests, SSR).
 *
 * The caller MUST pass the router's own location rather than letting this read
 * `window.location`. The router commits its state roughly 17ms before the
 * browser URL updates, so a caller keyed on the router's pathname would
 * evaluate against the *previous* URL. Signing in is exactly that case: the
 * router moves `/auth` → `/`, the effect runs twice while `window.location`
 * still reads `/auth` (a preserved path, so no redirect), and because the
 * router pathname never changes again the check is never re-asked — stranding
 * the phone on the desktop shell with no recovery short of a reload.
 */
export function shouldRedirectToMobileNow(location: {
  pathname: string;
  search: string;
}): boolean {
  if (typeof window === "undefined") return false;

  return shouldRedirectToMobile({
    pathname: location.pathname,
    search: location.search,
    width: window.innerWidth,
    coarsePointer: hasCoarsePointer(),
    // Falls back to the live URL for the opt-out only: a `?desktop=1` arriving
    // via a full page load is present in both, and the persisted flag is the
    // authority on subsequent navigations.
    optedOutOfMobile: readDesktopOptOut(location.search || window.location.search),
  });
}
