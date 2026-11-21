// Copyright (c) 2025 Reliant Labs

/**
 * A `reliant://open?url=<app path>` link, opened in the desktop app (control-plane
 * docs/design/unified-shell-and-projects.md §10.3): the main process parses
 * and checks it (electron/src/deep-link.js), surfaces the window the person
 * is in, and hands it the path ("open-route"); this navigates that window's
 * router there. The window then opens it like any other URL: a chat in its
 * org, or not found.
 *
 * Checked again here, the same way: the renderer does not trust what crossed
 * the process boundary to be a page of this app.
 */

/** Whether `route` is an app page a link may open: a chat (`/c/…`), any page under an org (`/o/…`), or the new-chat view. */
export function isOpenableRoute(route: string): boolean {
  // A control character or a backslash has no place in a link's path; the URL parser would quietly drop or turn them.
  // eslint-disable-next-line no-control-regex
  if (!route.startsWith("/") || route.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(route)) return false;
  let url: URL;
  try {
    url = new URL(route, "https://app.invalid");
  } catch {
    return false;
  }
  if (url.origin !== "https://app.invalid") return false;
  return url.pathname === "/new" || url.pathname.startsWith("/c/") || url.pathname.startsWith("/o/");
}

interface RouterLike {
  navigate: (options: { href: string }) => unknown;
}

/** Send `reliant://open?url=` links to `router`; returns the unsubscribe (a no-op outside the desktop app, or on one that predates them). */
export function listenForOpenRouteLinks(router: RouterLike): () => void {
  const subscribe = typeof window === "undefined" ? undefined : window.electronAPI?.onOpenRoute;
  if (!subscribe) return () => {};
  return subscribe((route) => {
    if (isOpenableRoute(route)) void router.navigate({ href: route });
  });
}
