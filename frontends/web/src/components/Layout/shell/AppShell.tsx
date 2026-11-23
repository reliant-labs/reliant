// Copyright (c) 2026 Reliant Labs

/**
 * THE ONE SHELL (U6, control-plane docs/design/unified-shell-and-projects.md
 * §4): one frame and one left nav around every app page — chats, the Inbox,
 * Projects, Workflows, an app's console, Usage, Settings — so moving between
 * them never remounts the nav (its scroll, its open groups, its chat list).
 *
 * WHERE IT MOUNTS. Above the route outlet, in the root route's component
 * (routes.tsx RootShell), deciding from the matched routes whether the page
 * on screen is an app page. It is not a layout route: a new parent between the
 * root and `_app` / `_authenticated` would rename their route ids, which the
 * keyboard host, `useSearch({ from: '/_app' })` and the onboarding route read.
 * And the two layouts are siblings, so a frame inside each would remount the
 * nav on every move between a chat and any other page.
 *
 * WHAT IT IS ON. Signed in, on the desktop surface, under one of the two app
 * layouts — never /auth, /onboarding, the phone's /m/*, the workflow builder
 * (its own full-screen chrome) or the public forge demo (its own console
 * frame). The same rule as the keyboard host's (Shortcuts/ShortcutHost.tsx),
 * read from the same rendered matches. Off it, the frame renders nothing of its own and
 * its children sit where they always did (`display: contents`), so turning it
 * on or off never remounts the page under it.
 *
 * THE NAV'S POSITIONS. Docked beside the page (resizable, its width kept);
 * collapsed (Header's toggle, the shortcut) it slides over the page from the
 * left edge while hovered; on a narrow window it is a drawer the page header's
 * toggle opens. It is ONE element in all three: only its positioning changes.
 */

import { Suspense, useCallback, useEffect, useMemo, type ReactNode } from "react";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { Menu } from "lucide-react";

import Tooltip from "@/components/forge-ui/tooltip";
import { useNarrowViewport } from "@/hooks/useNarrowViewport";
import { useShortcutHost } from "@/hooks/useShortcutHost";
import { useSidebarOverlay } from "@/hooks/useSidebarOverlay";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { lazyRoute } from "@/lib/lazyRoute";
import { runsPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { useAuthStore } from "@/store/authStore";
import { useShortcutLabel } from "@/store/shortcutsStore";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

import { ResizableSidebar } from "../ResizableSidebar";
import type { NavigateOptions } from "../sidebar/NavRenderer";
import { holdNavDrawerOpen, navDrawerHeld, releaseNavDrawerHold } from "../sidebar/navDrawerHold";
import { ShellFrameContext, shellFrameScope, toggleShellNav, useNavDrawer, useShellFrame, type ShellFrame } from "./shellFrame";

// The nav and its chat list load with the first shell page, not with the
// entry chunk every signed-out page pays for.
const Sidebar = lazyRoute(() => import("../Sidebar"), "Sidebar");

// ── The frame ───────────────────────────────────────────────────────────────

export function AppShellFrame({ children }: { children: ReactNode }) {
  // The RENDERED matches, like the keyboard host: mid-navigation the location
  // already names the destination while the page on screen is still the old one.
  const pathname = useRouterState({
    select: (s) => s.matches[s.matches.length - 1]?.pathname ?? s.location.pathname,
  });
  const routeIds = useRouterState({ select: (s) => s.matches.map((match) => match.routeId).join(" ") });
  const signedIn = useAuthStore((state) => state.user !== null);
  const inShell = shellFrameScope({ routeIds: routeIds.split(" "), pathname, signedIn });

  const expanded = useWorkspaceStateStore((state) => state.leftSidebarExpanded);
  const narrow = useNarrowViewport();
  const drawerOpen = useNavDrawer((state) => state.open);
  const setDrawerOpen = useNavDrawer((state) => state.setOpen);
  const docked = inShell && expanded && !narrow;
  // Collapsed on a wide window: the nav slides in while the left edge (then the nav) is hovered.
  const overlay = useSidebarOverlay({ isSidebarExpanded: !inShell || docked || narrow });
  const shown = docked || (narrow ? drawerOpen : overlay.isSidebarHovered);

  // A drawer closes when a row in it opens a page (unless the row drilled into
  // Agents or Observability: navDrawerHold), and when the window widens.
  // A drill that crossed from the phone's own screens (Mobile/MobileNavDrawer)
  // onto a shell page opens this drawer on the area's rows instead.
  useEffect(() => {
    setDrawerOpen(navDrawerHeld() && narrow);
    // `narrow` is read, not watched: widening closes the drawer below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pathname, setDrawerOpen]);
  useEffect(() => setDrawerOpen(false), [narrow, setDrawerOpen]);
  useEffect(() => {
    if (!narrow || !drawerOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) setDrawerOpen(false);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [narrow, drawerOpen, setDrawerOpen]);

  const frame = useMemo<ShellFrame>(() => ({ inShell, navDocked: docked }), [inShell, docked]);

  return (
    <ShellFrameContext.Provider value={frame}>
      <div
        className={inShell ? "relative flex h-dvh w-full overflow-hidden bg-background" : "contents"}
        data-shell-frame={inShell ? (docked ? "docked" : "floating") : undefined}
      >
        {inShell && !docked && !narrow && (
          <div
            aria-hidden="true"
            className="absolute inset-y-0 left-0 z-40 w-2"
            onMouseEnter={overlay.handleSidebarMouseEnter}
            onMouseLeave={overlay.handleSidebarMouseLeave}
          />
        )}
        {inShell && narrow && drawerOpen && (
          <div aria-hidden="true" className="absolute inset-0 z-40 bg-background/60" onClick={() => setDrawerOpen(false)} />
        )}
        {inShell && (
          <div
            data-testid="shell-nav"
            data-state={docked ? "docked" : shown ? "open" : "closed"}
            inert={!shown}
            className={
              docked
                ? "relative flex h-full shrink-0"
                : cn(
                    "absolute inset-y-0 left-0 z-50 flex h-full shadow-2xl transition-transform duration-200 ease-out motion-reduce:transition-none",
                    shown ? "translate-x-0" : "-translate-x-full",
                  )
            }
            onMouseEnter={!docked && !narrow ? overlay.handleOverlayMouseEnter : undefined}
            onMouseLeave={!docked && !narrow ? overlay.handleOverlayMouseLeave : undefined}
          >
            <ResizableSidebar storageKey="chat-sidebar-width">
              {/* The id is how the right panels leave room for the nav; only a docked nav takes any. */}
              <div id={docked ? "layout-left-sidebar" : undefined} className="flex h-full flex-col">
                <Suspense fallback={null}>
                  <ShellNav />
                </Suspense>
              </div>
            </ResizableSidebar>
          </div>
        )}
        {/* The shell's ONE main landmark: pages inside it render their body as a plain region (PageMain). */}
        <div role={inShell ? "main" : undefined} className={inShell ? "flex h-full min-w-0 flex-1 flex-col" : "contents"} data-testid={inShell ? "shell-main" : undefined}>
          {children}
        </div>
      </div>
    </ShellFrameContext.Provider>
  );
}

/** The nav, wired to the router and the keyboard host. */
function ShellNav() {
  const navigate = useNavigate();
  const shortcutHost = useShortcutHost();
  const location = useRouterState({ select: (s) => s.location });
  const { isMac, isFullscreen, dragRegionStyle, noDragRegionStyle } = useTitleBarChrome();
  const { navDocked } = useShellFrame();
  const toggleShortcut = useShortcutLabel("toggleSidebar");

  // A destination closes the narrow window's drawer, even one that is the page
  // already on screen; a drill-in keeps it open to show the area's rows.
  const onNavigate = useCallback(
    (href: string, options?: NavigateOptions) => {
      if (options?.drill) holdNavDrawerOpen();
      else {
        releaseNavDrawerHold();
        useNavDrawer.getState().setOpen(false);
      }
      void navigate({ href });
    },
    [navigate],
  );
  const onOpenChatSearch = useCallback(() => void shortcutHost?.run("onChatSearch"), [shortcutHost]);

  return (
    <>
      {/* macOS draws its traffic lights over the nav's top-left: a strip that
          moves the window, with the way to hide the nav at its right. */}
      {isMac && !isFullscreen && (
        <div className="flex h-[var(--shell-bar-h)] shrink-0 items-center justify-end px-2" style={dragRegionStyle}>
          {navDocked && (
            <Tooltip content={toggleShortcut ? `Hide sidebar (${toggleShortcut})` : "Hide sidebar"} side="bottom" delay={300}>
              <button
                type="button"
                onClick={toggleShellNav}
                aria-label="Hide sidebar"
                style={noDragRegionStyle}
                className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <Menu className="size-4" aria-hidden="true" />
              </button>
            </Tooltip>
          )}
        </div>
      )}
      <Sidebar
        pathname={location.pathname}
        search={location.search as Record<string, unknown>}
        onNavigate={onNavigate}
        onOpenRuns={(search) => void navigate({ to: runsPath(), search: search ?? {} })}
        onOpenChatSearch={onOpenChatSearch}
      />
    </>
  );
}
