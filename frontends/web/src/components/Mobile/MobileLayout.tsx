/**
 * Shell for every `/m/*` route.
 *
 * Owns the three things that make a route "mobile" rather than "narrow":
 *
 *   1. **Surface** — wraps children in `SurfaceProvider surface="mobile"`, so
 *      capability checks (`useCapability`) and surface-aware defaults (tool
 *      call collapse) resolve correctly anywhere below here.
 *   2. **Safe areas** — honours the iOS notch/home-indicator insets, which
 *      the desktop shell has no reason to think about.
 *   3. **Overlay opt-out** — see below.
 *
 * ## Why the root overlays are suppressed
 *
 * `rootRoute` mounts `ModalLayer`, `OnboardingWizard`, `ContextualTipsLayer`,
 * `AnonSignInNudge` and `GitHubSyncStatus` on *every* route, deliberately —
 * before that, users on `/settings` and `/workflow/*` silently got no toasts
 * and no modals. That reasoning is sound for desktop routes and wrong for
 * this one:
 *
 *   - `OnboardingWizard` is the guided *tour*, and it spotlights DOM that
 *     exists only in the desktop shell (`[data-onboarding='left-sidebar']`,
 *     `'right-sidebar'`, `'workflow-canvas'`). On a phone those selectors
 *     never match, so it would render "Open <page> to continue" prompts
 *     pointing at pages the mobile surface does not have.
 *   - `ContextualTipsLayer` and `AnonSignInNudge` are coachmarks positioned
 *     against desktop chrome for the same reason.
 *
 * `<Toaster />` is the deliberate exception — it is a portal with no anchor,
 * and suppressing it would mean send failures fail silently. It stays.
 *
 * The suppression is *not* done by unmounting the root overlays (they're
 * shared with every other route); each one gates itself on `useSurface()`.
 */

import type { ReactNode } from "react";
import { useEffect } from "react";
import { SurfaceProvider } from "../../lib/surfaceContext";
import { applyRootFontSize, DEFAULT_FONT_SIZE } from "../../lib/rootFontSize";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";

export function MobileLayout({ children }: { children: ReactNode }) {
  // The pre-hydration bootstrap in index.html only runs on a full page load,
  // but users reach /m/* by client-side redirect — so without this the mobile
  // type scale would only apply to hard navigations. Re-applying on mount (and
  // restoring on unmount) keeps desktop unaffected when navigating back out.
  useEffect(() => {
    const stored = settingsSync.getSetting(SETTINGS_KEYS.FONT_SIZE, DEFAULT_FONT_SIZE);
    applyRootFontSize(stored);
    return () => {
      // Runs after the route has already left /m/*, so this resolves the
      // desktop map.
      applyRootFontSize(stored);
    };
  }, []);

  return (
    <SurfaceProvider surface="mobile">
      {/*
        `100dvh` rather than `100vh`: on mobile Safari the visual viewport
        shrinks when the URL bar is shown and `vh` does not follow, which puts
        a sticky composer underneath the browser chrome. `dvh` tracks it.
      */}
      <div
        className="flex h-[100dvh] w-full flex-col overflow-hidden bg-background text-foreground"
        style={{
          // Top inset keeps content clear of the notch. Inline because these
          // are `env()` values Tailwind has no token for.
          paddingTop: "env(safe-area-inset-top)",
          // The BOTTOM inset is deliberately not applied here. Padding on this
          // container would stop the tab bar's background short of the screen
          // edge, leaving a strip of page behind the home indicator. The bar
          // absorbs the inset itself so its background reaches the edge while
          // its tappable row stays above the indicator.
        }}
        data-surface="mobile"
      >
        {children}
      </div>
    </SurfaceProvider>
  );
}
