/**
 * Whether this browser is phone-shaped: narrow AND touch. The one definition
 * of a phone, read by the redirect to the phone surface (lib/mobileRedirect)
 * and by the root font size (lib/rootFontSize), so the two can never disagree
 * about what a phone is.
 *
 * Import-free on purpose: rootFontSize is read at boot and from the settings
 * services, and mobileRedirect's own imports (paths, the org stores) must not
 * come along with it.
 *
 * index.html's pre-hydration bootstrap holds a copy (it runs before any
 * module loads); rootFontSize.test.ts runs it against the same cases.
 */

/** Below this width the desktop shell's sidebars and panels stop fitting. */
export const PHONE_MAX_WIDTH = 768;

/**
 * `pointer: coarse` is the touch signal; `hover: none` catches devices that
 * report a fine pointer (some styluses) but have no hover.
 */
export const PHONE_POINTER_QUERY = "(pointer: coarse), (hover: none)";

/** Query param and storage key for the "give me the desktop app" opt-out. */
export const DESKTOP_OPT_OUT_PARAM = "desktop";
export const DESKTOP_OPT_OUT_KEY = "reliant-force-desktop";

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

/** True when the primary input is touch-like (no hover, coarse pointer). */
export function hasCoarsePointer(): boolean {
  if (typeof window === "undefined") return false;
  return window.matchMedia?.(PHONE_POINTER_QUERY).matches ?? false;
}

/**
 * Whether this device is phone-shaped — narrow AND touch, the same two
 * signals the redirect uses. For desktop-surface routes a phone can still be
 * on (the Apps console), to keep desktop-only overlays off them.
 */
export function isPhoneViewport(): boolean {
  if (typeof window === "undefined") return false;
  return window.innerWidth <= PHONE_MAX_WIDTH && hasCoarsePointer();
}

/**
 * A phone that has not asked for the desktop app (`?desktop=1`): the device
 * the phone surface is for, wherever in the app it is.
 */
export function isPhoneDevice(search: string = typeof window !== "undefined" ? window.location.search : ""): boolean {
  return isPhoneViewport() && !readDesktopOptOut(search);
}
