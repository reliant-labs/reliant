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

/** Below this width the desktop shell's sidebars and panels stop fitting. */
export const MOBILE_MAX_WIDTH = 768;

/** Query param and storage key for the "give me the desktop app" opt-out. */
export const DESKTOP_OPT_OUT_PARAM = "desktop";
const DESKTOP_OPT_OUT_KEY = "reliant-force-desktop";

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
  const { pathname, width, coarsePointer, optedOutOfMobile } = env;

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
  // phone — which is what this list used to do. /apps, the list of apps the
  // console opens from, is one responsive page for the same reason.
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
    "/design-sandbox",
    "/forge",
    "/apps",
  ];
  if (PRESERVED.some((p) => pathname === p || pathname.startsWith(`${p}/`))) {
    return false;
  }

  return width <= MOBILE_MAX_WIDTH && coarsePointer;
}

/**
 * Where a redirected desktop path lands on the mobile surface.
 *
 * A redirect that always went to the chat list threw away what the link was
 * about: an Inbox notification, a run link a teammate pasted, a "go to
 * settings" prompt all became the chat list. Each desktop path with a mobile
 * twin maps to it; everything else still lands on the chat list, the mobile
 * home. A run IS a chat, so a run link opens that chat.
 */
export function mobilePathFor(pathname: string, search = ""): string {
  const path = pathname.replace(/\/+$/, "") || "/";
  if (path === "/inbox" || path.startsWith("/inbox/")) return "/m/inbox";

  const run = /^\/workflows\/runs\/([^/]+)/.exec(path);
  if (run) return `/m/chats/${run[1]}`;
  if (path === "/workflows/runs") return "/m/runs";
  // A workflow's page (where its triggers are — what an automation link
  // opens) is its read-only viewer; an automation id is resolved first.
  const workflow = /^\/workflows\/library\/([^/]+)/.exec(path);
  if (workflow) return `/m/workflows/${workflow[1]}`;
  const automation = /^\/(?:workflows\/)?automations\/([^/]+)/.exec(path);
  if (automation) return `/m/automations/${automation[1]}`;
  if (path === "/workflows" || path.startsWith("/workflows/")) return "/m/workflows";

  if (path.startsWith("/settings/machines") || path.startsWith("/settings/environments")) return "/m/daemons";
  // Billing on a phone is a screen inside /m/settings. A visit that has a way
  // back (the claim page sends people here to subscribe) keeps it: the mobile
  // twin opens on billing and offers the same return.
  if (path === "/settings/billing") {
    const q = new URLSearchParams(search);
    const returnTo = q.get("returnTo");
    if (returnTo && returnTo.startsWith("/") && !returnTo.startsWith("//")) {
      return `/m/settings?${new URLSearchParams({ section: "billing", returnTo })}`;
    }
  }
  if (path === "/settings" || path.startsWith("/settings/")) return "/m/settings";

  return "/m/chats";
}

/** Reads the opt-out from the URL, persisting it so it survives navigation. */
export function readDesktopOptOut(search: string): boolean {
  try {
    if (new URLSearchParams(search).has(DESKTOP_OPT_OUT_PARAM)) {
      localStorage.setItem(DESKTOP_OPT_OUT_KEY, "1");
      return true;
    }
    return localStorage.getItem(DESKTOP_OPT_OUT_KEY) === "1";
  } catch {
    // Safari private browsing throws on localStorage. Honour the URL for this
    // navigation and accept that it won't persist.
    return new URLSearchParams(search).has(DESKTOP_OPT_OUT_PARAM);
  }
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
 * Whether this device is phone-shaped — narrow AND touch, the same two
 * signals the redirect uses. For desktop-surface routes a phone can still be
 * on (the Apps console), to keep desktop-only overlays off them.
 */
export function isPhoneViewport(): boolean {
  if (typeof window === "undefined") return false;
  const coarsePointer =
    window.matchMedia?.("(pointer: coarse), (hover: none)").matches ?? false;
  return window.innerWidth <= MOBILE_MAX_WIDTH && coarsePointer;
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

  // `pointer: coarse` is the touch signal; `hover: none` catches devices that
  // report a fine pointer (some styluses) but have no hover.
  const coarsePointer =
    window.matchMedia?.("(pointer: coarse), (hover: none)").matches ?? false;

  return shouldRedirectToMobile({
    pathname: location.pathname,
    search: location.search,
    width: window.innerWidth,
    coarsePointer,
    // Falls back to the live URL for the opt-out only: a `?desktop=1` arriving
    // via a full page load is present in both, and the persisted flag is the
    // authority on subsequent navigations.
    optedOutOfMobile: readDesktopOptOut(location.search || window.location.search),
  });
}
