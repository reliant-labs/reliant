/**
 * Should Monaco be warmed at startup for this URL?
 *
 * `main.tsx` preloads Monaco before React mounts, so the editor is warm by the
 * time someone opens a file or the workflow builder. The preload pulls the
 * editor from a CDN (see `monacoManager`), which is multiple megabytes — worth
 * it on a route that will render an editor, pure waste on one that never will.
 *
 * Two kinds of route are excluded:
 *
 *   - **The mobile surface (`/m/*`).** No phone screen has an editor or a
 *     Monaco diff; mobile file and approval views render through the
 *     Prism-based `LightweightDiffViewer`.
 *   - **Unauthenticated entry routes.** `/oauth/consent` in particular is
 *     reached mid-OAuth from a third-party client, often on a phone, by
 *     someone who has not otherwise opened the app. Its time-to-interactive is
 *     a step in someone else's flow, and it is a consent form — there is no
 *     editor on it, and no navigation from it into one.
 *
 * This runs before the router exists, so it reads `window.location.pathname`
 * directly and must stay a pure function of the path.
 */

import { surfaceForPath } from "./surface";

/**
 * Route prefixes that never render an editor and are commonly someone's first
 * (or only) page load. Matched as full segments so `/upgraded` does not match
 * `/upgrade`.
 */
const EDITOR_FREE_PREFIXES = [
  "/auth",
  "/oauth",
  "/reset-password",
  "/verify-email",
  "/upgrade",
] as const;

/** True when `pathname` is `prefix` or a path segment beneath it. */
function isUnder(pathname: string, prefix: string): boolean {
  return pathname === prefix || pathname.startsWith(prefix + "/");
}

export function shouldPreloadMonaco(pathname: string): boolean {
  if (surfaceForPath(pathname) !== "desktop") return false;
  return !EDITOR_FREE_PREFIXES.some((prefix) => isUnder(pathname, prefix));
}
