/**
 * Keeps a tap on the mobile surface from feeling dead while a screen loads.
 *
 * ## Why taps felt slow
 *
 * Every `/m/*` screen is a `lazyRouteComponent`, and TanStack Router does not
 * commit a navigation until the destination's chunk has loaded (`runLoader`
 * awaits `route._componentsPromise`). The router has no `defaultPreload` and
 * no pending component. So the first tap on any destination left the old
 * screen on display, unchanged and with no feedback, for the whole chunk
 * download. The nav drawer was worse: it closes on the URL change, which
 * happens immediately, so the user saw the drawer close back onto the same
 * screen they had just tried to leave.
 *
 * The worst case is the first tap into a chat. `/m/chats/$chatId` renders the
 * shared `ChatContainer`, whose chunk is ~438 KB (~122 KB gzipped) before
 * TasksPanel and the rest. On a phone over cellular that is seconds.
 *
 * ## What this does
 *
 *   1. **Warms every mobile screen's chunk** once the shell is up and the
 *      browser is idle, so a tap has nothing to wait for. Skipped under
 *      Save-Data and while offline. A preload that fails while offline would
 *      fail the same way at tap time, and `lazyRouteComponent` keeps the
 *      first failure, so there is no reason to spend it early.
 *   2. **Shows a progress bar** at the top while any navigation is still
 *      pending: a cold start, a slow network, or a tap that lands before
 *      warm-up finishes. It appears after a short delay so a fast (warm)
 *      navigation never flashes it.
 */

import { useEffect, useState } from "react";
import { useMatch, useRouter, useRouterState } from "@tanstack/react-router";

/** Long enough that a warm navigation finishes first; short enough to beat "is it broken?". */
export const PROGRESS_DELAY_MS = 150;

type RouteNode = { children?: unknown };

/** Every route below `route`, depth-first: the screens a layout can render. */
export function descendantRoutes<T extends RouteNode>(route: T | undefined): T[] {
  if (!route || !Array.isArray(route.children)) return [];
  return (route.children as T[]).flatMap((child) => [child, ...descendantRoutes(child)]);
}

/**
 * Run `fn` when the browser is idle. iOS Safari, where this matters most, has
 * no `requestIdleCallback`, so it falls back to a short timeout that leaves
 * the first screen's own data requests a head start.
 */
function whenIdle(fn: () => void): () => void {
  if (typeof window.requestIdleCallback === "function") {
    const id = window.requestIdleCallback(fn, { timeout: 2000 });
    return () => window.cancelIdleCallback(id);
  }
  const id = window.setTimeout(fn, 1000);
  return () => window.clearTimeout(id);
}

function shouldSkipWarmup(): boolean {
  if (typeof navigator === "undefined") return true;
  if (navigator.onLine === false) return true;
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } })
    .connection;
  return connection?.saveData === true;
}

/** Load the chunk of every screen under the shell's own route. */
export function useMobileRouteWarmup() {
  const router = useRouter();
  // The nearest match inside MobileShell is the shell's own layout route, so
  // its descendants are exactly the screens this surface can show. No route
  // id or path is hard-coded here.
  const shellRouteId = useMatch({ strict: false, select: (match) => match.routeId });

  useEffect(() => {
    if (!shellRouteId || shouldSkipWarmup()) return;
    return whenIdle(() => {
      const shell = (router.routesById as Record<string, RouteNode>)[shellRouteId];
      for (const route of descendantRoutes(shell)) {
        // Fire-and-forget. `lazyRouteComponent` records its own failure and
        // the router surfaces it if that screen is actually opened.
        void router
          .loadRouteChunk(route as Parameters<typeof router.loadRouteChunk>[0])
          .catch(() => {});
      }
    });
  }, [router, shellRouteId]);
}

export function MobileNavigationFeedback() {
  useMobileRouteWarmup();

  const isLoading = useRouterState({ select: (s) => s.isLoading });
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    if (!isLoading) {
      setVisible(false);
      return;
    }
    const id = window.setTimeout(() => setVisible(true), PROGRESS_DELAY_MS);
    return () => window.clearTimeout(id);
  }, [isLoading]);

  if (!visible) return null;

  return (
    <div
      role="progressbar"
      aria-label="Loading"
      // Above the drawer and the sheets (`z-[9999]`), and never in the way
      // of a tap.
      className="pointer-events-none fixed inset-x-0 z-[10000] h-0.5 overflow-hidden"
      style={{ top: "env(safe-area-inset-top)" }}
    >
      <div className="h-full w-full animate-pulse bg-primary" />
    </div>
  );
}
