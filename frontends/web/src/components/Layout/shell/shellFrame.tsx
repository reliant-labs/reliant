// Copyright (c) 2026 Reliant Labs

/**
 * What a page and the shell's controls know about the one shell (AppShell.tsx):
 * whether the page on screen is in it, whether its nav is docked, and how to
 * show or hide the nav. Kept apart from the frame so a page reads it without
 * loading the nav.
 */

import { createContext, useContext, type CSSProperties, type HTMLAttributes, type ReactNode } from "react";
import { Menu } from "lucide-react";

import Tooltip from "@/components/forge-ui/tooltip";
import { NARROW_VIEWPORT_QUERY } from "@/hooks/useNarrowViewport";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { clearPane, focusPane, focusPrevious, isFocusWithin } from "@/lib/keyboard/focus";
import { orgFreeRouteId } from "@/lib/paths/org";
import { surfaceForPath } from "@/lib/surface";
import { cn } from "@/lib/utils";
import { useShortcutLabel } from "@/store/shortcutsStore";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

import { useNavDrawer } from "./navDrawer";

// ── Where the frame is on ───────────────────────────────────────────────────

/** TanStack route ids of the two auth-guarded app layouts (routes.tsx). */
const APP_LAYOUTS = new Set(["/_app", "/_authenticated"]);
/**
 * Under `_authenticated`, but not a page of the shell: onboarding (a setup
 * flow), the phone surface and the workflow builder (a full-screen editor
 * with its own header). The forge demo is a public root route with its own
 * console frame, under neither layout.
 */
const OUTSIDE_SHELL = new Set([
  "/_authenticated/onboarding",
  "/_authenticated/_mobile",
  "/_authenticated/workflow/new",
  "/_authenticated/workflow/$workflowName",
]);

/**
 * Whether the page with these matched routes is a page of the one shell.
 * Decided by the route tree, not by path: any page mounted under the two app
 * layouts is one (the chat URLs of U5, `/c/<chat>` and `/new`, included), so
 * the frame never depends on how the workspace picks the open chat.
 */
export function shellFrameScope({
  routeIds,
  pathname,
  signedIn,
}: {
  routeIds: readonly string[];
  pathname: string;
  signedIn: boolean;
}): boolean {
  // The org tree mounts the same layouts under `/o/$org` (routes.tsx).
  const ids = routeIds.map(orgFreeRouteId);
  return (
    signedIn &&
    surfaceForPath(pathname) === "desktop" &&
    ids.some((id) => APP_LAYOUTS.has(id)) &&
    !ids.some((id) => OUTSIDE_SHELL.has(id))
  );
}

// ── What a page reads about the frame ───────────────────────────────────────

export interface ShellFrame {
  /** The page is inside the one shell: the nav and the way around are the shell's. */
  inShell: boolean;
  /** The nav is docked beside the page, so the page does not reach the window's left edge. */
  navDocked: boolean;
}

export const ShellFrameContext = createContext<ShellFrame>({ inShell: false, navDocked: false });

export function useShellFrame(): ShellFrame {
  return useContext(ShellFrameContext);
}

/**
 * A page's body region. Inside the one shell the frame is the page's ONE
 * `main` landmark (AppShellFrame), so the body is a plain region; outside it
 * (the forge host before it runs the shell, a preview) the body is the main
 * landmark itself. A div with a role either way, so the shell turning on or
 * off never changes the element and never remounts the page.
 */
export function PageMain({ children, ...props }: HTMLAttributes<HTMLDivElement> & { children?: ReactNode }) {
  const { inShell } = useShellFrame();
  return (
    <div role={inShell ? undefined : "main"} {...props}>
      {children}
    </div>
  );
}

// ── Opening and closing the nav ─────────────────────────────────────────────

/** The narrow window's drawer: open or not. Local to the frame, never persisted (navDrawer.ts). */
export { useNavDrawer };

function narrowViewport(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia?.(NARROW_VIEWPORT_QUERY).matches;
}

/**
 * Show or hide the nav: dock or collapse it (remembered, `leftSidebarExpanded`),
 * or on a narrow window open or close its drawer. Header's toggle, the
 * shortcut and the page headers' toggle all come here.
 */
export function toggleShellNav(): void {
  if (narrowViewport()) {
    const drawer = useNavDrawer.getState();
    drawer.setOpen(!drawer.open);
    return;
  }
  const state = useWorkspaceStateStore.getState();
  const next = !state.leftSidebarExpanded;
  // Closing a pane the cursor is inside would strand focus on a detached element.
  if (!next && isFocusWithin("left-sidebar")) {
    clearPane("left-sidebar");
    focusPrevious();
  }
  state.setLeftSidebarExpandedGlobal(next);
}

/** Show the nav and move focus into it (the focus-the-sidebar shortcut). */
export function revealShellNav(): void {
  if (narrowViewport()) useNavDrawer.getState().setOpen(true);
  else useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true);
  // After the commit that un-inerts it.
  requestAnimationFrame(() => focusPane("left-sidebar"));
}

/** The page header's way to the nav while it is not docked; nothing while it is. */
export function ShellNavToggle({ style }: { style?: CSSProperties }) {
  const { inShell, navDocked } = useShellFrame();
  const shortcut = useShortcutLabel("toggleSidebar");
  if (!inShell || navDocked) return null;
  return (
    <Tooltip content={shortcut ? `Show sidebar (${shortcut})` : "Show sidebar"} side="bottom" delay={300}>
      <button
        type="button"
        onClick={toggleShellNav}
        aria-label="Show sidebar"
        data-testid="shell-nav-toggle"
        style={style}
        className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Menu className="size-4" aria-hidden="true" />
      </button>
    </Tooltip>
  );
}

/**
 * A page's top bar inside the shell: 48px like every shell bar, on the shell's
 * one surface with no rule under it, the nav's toggle when the nav is not
 * docked, and the window's drag region in the desktop app (clearing the macOS
 * traffic lights when the page reaches the window's left edge).
 */
export function ShellPageHeader({ children, actions, testId }: { children: ReactNode; actions?: ReactNode; testId?: string }) {
  const { navDocked } = useShellFrame();
  const { isElectron, trafficLightPadding, dragRegionStyle, noDragRegionStyle } = useTitleBarChrome({
    alignedToWindowEdge: !navDocked,
    collapsedPadding: navDocked ? "32px" : "12px",
  });
  return (
    <header
      className={cn("flex h-[var(--shell-bar-h)] shrink-0 select-none items-center gap-2 pr-4", isElectron && "cursor-move")}
      style={{ ...dragRegionStyle, paddingLeft: trafficLightPadding }}
      data-testid={testId}
    >
      <ShellNavToggle style={noDragRegionStyle} />
      <div className="flex min-w-0 cursor-default items-center gap-3" style={noDragRegionStyle}>
        {children}
      </div>
      <div className="min-w-4 flex-1 self-stretch" />
      {actions && (
        <div className="flex shrink-0 cursor-default items-center gap-2" style={noDragRegionStyle}>
          {actions}
        </div>
      )}
    </header>
  );
}
