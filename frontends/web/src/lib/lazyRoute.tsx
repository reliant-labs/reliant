// Copyright (c) 2025 Reliant Labs

/**
 * Code-split route components that survive a deploy.
 *
 * Every screen is its own chunk, named by content hash. A tab opened before a
 * deploy still asks for the previous build's chunks, and once those are gone
 * the static host answers index.html, so the import rejects ("Failed to fetch
 * dynamically imported module"). Reloading is the right recovery: the new
 * index.html names chunks that exist.
 *
 * TanStack's lazyRouteComponent reloads too, but from inside RENDER: every
 * render of the route after the failure calls `window.location.reload()`. The
 * browser runs `beforeunload` listeners synchronously inside that call, so a
 * listener that touches React state schedules a render that renders the route
 * again, which reloads again. That is how opening the Inbox froze the tab in
 * ELECTRON-CC.
 *
 * So the failed import never reaches TanStack. It resolves instead to a stand-in
 * screen that reloads from an effect — once, after it commits, whatever renders
 * follow. Nothing reloads until the screen is actually shown: the mobile shell
 * warms every screen's chunk in the background, and a stale chunk found that
 * way must not reload the page out from under the user.
 */

import { useEffect } from "react";
import { lazyRouteComponent } from "@tanstack/react-router";

const RELOAD_KEY_PREFIX = "reliant:stale-chunk-reload:";

/** How a browser — or Vite's preload helper — rejects a chunk it cannot load. */
export function isChunkLoadError(error: unknown): error is Error {
  if (!(error instanceof Error)) return false;
  const { message } = error;
  return (
    message.startsWith("Failed to fetch dynamically imported module") || // Chromium
    message.startsWith("error loading dynamically imported module") || // Firefox
    message.startsWith("Importing a module script failed") || // Safari
    message.startsWith("Unable to preload CSS") // Vite's __vitePreload
  );
}

/**
 * A chunk failed again after the reload that should have fixed it. Its message
 * deliberately matches none of the patterns above: TanStack answers those with
 * its own reload-in-render, and this belongs to the route's error boundary.
 */
export class StaleBuildError extends Error {
  constructor(cause: Error) {
    super(
      "This screen couldn't be loaded. Reliant may have been updated since this tab opened — reload to get the latest version.",
      { cause },
    );
    this.name = "StaleBuildError";
  }
}

/**
 * Claim this page's one reload for a missing chunk. One per chunk per session:
 * the reload loads a new build whose chunks have new names, so the same chunk
 * failing again means reloading does not fix it. Without sessionStorage nothing
 * would bound the reloads, so none is claimed.
 */
function claimReload(error: Error): boolean {
  const key = RELOAD_KEY_PREFIX + error.message;
  try {
    if (sessionStorage.getItem(key) !== null) return false;
    sessionStorage.setItem(key, "1");
    return true;
  } catch {
    return false;
  }
}

function ReloadingScreen() {
  useEffect(() => {
    window.location.reload();
  }, []);
  return (
    <div className="flex h-screen items-center justify-center bg-background text-sm text-muted-foreground" role="status">
      Reliant was updated. Reloading…
    </div>
  );
}

/** The module a route import resolves to when its chunk is gone. */
export function staleChunkModule(error: Error, exportName: PropertyKey) {
  const Screen = claimReload(error)
    ? ReloadingScreen
    : function StaleBuildScreen(): never {
        throw new StaleBuildError(error);
      };
  return { [exportName]: Screen };
}

/**
 * lazyRouteComponent, with a stale chunk handled by a screen that reloads from
 * an effect rather than by TanStack's reload during render. Use it for every
 * code-split route component.
 */
export function lazyRoute<T extends Record<string, any>, TKey extends keyof T = "default">(
  importer: () => Promise<T>,
  exportName?: TKey,
) {
  return lazyRouteComponent<T, TKey>(
    () =>
      importer().catch((error: unknown) => {
        if (!isChunkLoadError(error)) throw error;
        return staleChunkModule(error, exportName ?? "default") as unknown as T;
      }),
    exportName,
  );
}
