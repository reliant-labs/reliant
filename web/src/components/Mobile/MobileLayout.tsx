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
import { useMobileViewport } from "../../hooks/useMobileViewport";
import type { CSSProperties } from "react";
import { SurfaceProvider } from "../../lib/surfaceContext";
import { applyRootFontSize, DEFAULT_FONT_SIZE } from "../../lib/rootFontSize";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";

export function MobileLayout({ children }: { children: ReactNode }) {
  const viewport = useMobileViewport();
  // Safari can scroll the document while revealing a focused input even when
  // the chat itself fits. Keep document scrolling separate from list scrolling.
  useEffect(() => {
    const targets = [
      { element: document.documentElement, properties: { overflow: "hidden", height: "100%", "overscroll-behavior": "none" } },
      { element: document.body, properties: { position: "fixed", top: "0", right: "0", bottom: "0", left: "0", width: "100%", height: "100%", overflow: "hidden", "overscroll-behavior": "none" } },
    ];
    const previous = targets.flatMap(({ element, properties }) => Object.entries(properties).map(([name, value]) => {
      const saved = { element, name, value: element.style.getPropertyValue(name), priority: element.style.getPropertyPriority(name) };
      element.style.setProperty(name, value);
      return saved;
    }));
    return () => previous.forEach(({ element, name, value, priority }) => {
      if (value) element.style.setProperty(name, value, priority);
      else element.style.removeProperty(name);
    });
  }, []);
  useEffect(() => {
    const style = document.documentElement.style;
    const previous = style.getPropertyValue("--mobile-bottom-inset");
    style.setProperty("--mobile-bottom-inset", viewport.keyboardVisible ? "0px" : "env(safe-area-inset-bottom)");
    return () => {
      if (previous) style.setProperty("--mobile-bottom-inset", previous);
      else style.removeProperty("--mobile-bottom-inset");
    };
  }, [viewport.keyboardVisible]);
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
        The fixed frame tracks visualViewport rather than allowing Safari to
        pan a scrolling document past the composer when the keyboard opens.
      */}
      <div
        className="fixed flex h-[100dvh] w-full flex-col overflow-hidden overscroll-none bg-background text-foreground"
        style={{
          // Top inset keeps content clear of the notch. Inline because these
          // are `env()` values Tailwind has no token for.
          paddingTop: "env(safe-area-inset-top)",
          top: viewport.top,
          left: viewport.left,
          width: viewport.width,
          height: viewport.height,
          paddingLeft: "env(safe-area-inset-left)",
          paddingRight: "env(safe-area-inset-right)",
          "--mobile-viewport-height": `${viewport.height}px`,
          // The BOTTOM inset is deliberately not applied here. Padding on this
          // container would stop the tab bar's background short of the screen
          // edge, leaving a strip of page behind the home indicator. The bar
          // absorbs the inset itself so its background reaches the edge while
          // its tappable row stays above the indicator.
        } as CSSProperties}
        data-keyboard-visible={viewport.keyboardVisible || undefined}
        data-surface="mobile"
      >
        {children}
      </div>
    </SurfaceProvider>
  );
}
