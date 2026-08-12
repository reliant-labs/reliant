/**
 * The document's root font size, which every rem-based Tailwind class
 * resolves against.
 *
 * Two things make this worth centralising.
 *
 * **It is a user preference, not a constant.** The Appearance settings expose
 * five steps, so nothing may hardcode a pixel value — a change here has to
 * shift the whole scale, not replace it.
 *
 * **The mobile surface needs a bigger base.** Tailwind's scale is rem-based,
 * so at the 14px desktop default `text-sm` renders at 13.1px and `text-xs` at
 * 11.4px. iOS body text is ~17px. That gap is the main reason the phone UI
 * reads as a cramped web page rather than an app, and it also flattens
 * hierarchy: a screen title in `text-sm` is the same size as the rows beneath
 * it. Shifting the base two steps up for `/m/*` fixes every screen at once and
 * keeps the user's relative preference intact.
 *
 * The same maps are duplicated in `index.html`'s pre-hydration bootstrap so
 * there is no flash of small text on a cold load; keep them in sync.
 */

import { surfaceForPath } from "./surface";

export const FONT_SIZE_MAP: Record<string, string> = {
  xs: "12px",
  sm: "13px",
  md: "14px",
  lg: "15px",
  xl: "16px",
};

/** Two steps up from the desktop map — see the module comment. */
export const MOBILE_FONT_SIZE_MAP: Record<string, string> = {
  xs: "14px",
  sm: "15px",
  md: "16px",
  lg: "17px",
  xl: "18px",
};

/**
 * Root font size for a preference step on a given path.
 *
 * Path-based rather than viewport-based, matching `surfaceForPath`: the URL is
 * the explicit signal for which surface is rendering, and a narrow desktop
 * window should keep desktop sizing.
 */
export function rootFontSizeFor(fontSize: string, pathname: string): string {
  const map =
    surfaceForPath(pathname) === "mobile" ? MOBILE_FONT_SIZE_MAP : FONT_SIZE_MAP;
  return map[fontSize] ?? map.md;
}

/**
 * Apply the root font size for the current location.
 *
 * Call after any navigation that may cross the desktop/mobile boundary — the
 * pre-hydration bootstrap only runs on a full page load, and users reach
 * `/m/*` by client-side redirect.
 */
export function applyRootFontSize(fontSize: string): void {
  if (typeof document === "undefined") return;
  document.documentElement.style.fontSize = rootFontSizeFor(
    fontSize,
    window.location.pathname,
  );
}
