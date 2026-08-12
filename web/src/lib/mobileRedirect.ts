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
  const PRESERVED = [
    "/auth",
    "/oauth",
    "/onboarding",
    "/reset-password",
    "/verify-email",
    "/upgrade",
    "/design-sandbox",
  ];
  if (PRESERVED.some((p) => pathname === p || pathname.startsWith(`${p}/`))) {
    return false;
  }

  return width <= MOBILE_MAX_WIDTH && coarsePointer;
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
