// Copyright (c) 2025 Reliant Labs

/**
 * Chrome for an app area that lives outside the chat workspace — the Inbox,
 * Apps, Projects. Inside the one shell (shell/AppShell.tsx) the nav is the way
 * around, so the area is just a slim page bar (the nav's toggle while the nav
 * is not docked, the way back from a detail page) and its content; Escape
 * still steps a detail page back to its list. Outside it — a host with no
 * shell — it keeps its own title bar with the way out.
 *
 * These routes sit under the bare `_authenticated` layout, which renders no
 * app chrome (the same position /settings and /forge are in), so without this
 * a user who opened the page could only leave by editing the URL. The exit and
 * the Escape binding mirror SettingsHeader/SettingsPage.
 */

import { useCallback, useEffect, type ReactNode } from "react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, X } from "lucide-react";

import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";
import { cn } from "@/lib/utils";
import ErrorBoundary from "../ErrorBoundary";
import Tooltip from "../forge-ui/tooltip";
import { OrgSwitcher } from "../Orgs/OrgSwitcher";
import { LearnLink, type LearnSection } from "@/lib/learnLinks";
import { orgFreePath } from "@/lib/paths/org";
import { PageMain, ShellPageHeader, useShellFrame } from "./shell/shellFrame";

interface AreaShellProps {
  /** The area's list path, e.g. "/inbox". Anything below it is a detail page. */
  areaPath: string;
  /** What the detail page's exit returns to, e.g. "All runs". */
  areaLabel: string;
  /** Lower-case noun for the list page's exit tooltip, e.g. "inbox". */
  areaNoun: string;
  /** The guide the area's bar links to (lib/learnLinks.tsx), if any. */
  learn?: LearnSection;
  /** A page that needs the width (a board, a calendar): up to 7xl, with a phone's narrower gutter. */
  wide?: boolean;
  children: ReactNode;
}

export function AreaShell(props: AreaShellProps) {
  const { inShell } = useShellFrame();
  return inShell ? <ShellArea {...props} /> : <StandaloneArea {...props} />;
}

/** The area inside the one shell: no exit, no org switcher, no title-bar chrome of its own. */
function ShellArea({ areaPath, areaLabel, learn, wide, children }: AreaShellProps) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const isDetail = orgFreePath(pathname).startsWith(`${orgFreePath(areaPath)}/`);
  const onBack = useCallback(() => {
    void navigate(getParentRouteNavigateOptions(pathname));
  }, [navigate, pathname]);
  // A detail page steps back to its list on Escape; a list has nowhere to leave to.
  useEscape(isDetail ? onBack : null);

  return (
    <div className="flex h-full min-h-0 w-full flex-col bg-background" data-testid="area-shell">
      <ShellPageHeader actions={learn ? <LearnLink section={learn} /> : undefined}>
        {isDetail && (
          <button
            type="button"
            onClick={onBack}
            className="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-ui text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            aria-label={`Back to ${areaLabel.toLowerCase()}`}
          >
            <ArrowLeft className="size-4" aria-hidden="true" />
            <span>{areaLabel}</span>
          </button>
        )}
      </ShellPageHeader>
      <PageMain className="relative min-h-0 flex-1 overflow-y-auto">
        <div className={cn("mx-auto w-full pb-12 pt-2", wide ? "max-w-7xl px-4 sm:px-8" : "max-w-5xl px-8")}>
          <ErrorBoundary layout="panel">{children}</ErrorBoundary>
        </div>
      </PageMain>
    </div>
  );
}

/** Escape runs `onEscape`, except in a field or under an open dialog, which own it. */
function useEscape(onEscape: (() => void) | null) {
  useEffect(() => {
    if (!onEscape) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      const target = event.target as HTMLElement | null;
      if (
        target?.tagName === "INPUT" ||
        target?.tagName === "TEXTAREA" ||
        target?.tagName === "SELECT" ||
        target?.isContentEditable
      ) {
        return;
      }
      if (document.querySelector('[aria-modal="true"]')) return;
      event.preventDefault();
      onEscape();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onEscape]);
}

function StandaloneArea({ areaPath, areaLabel, areaNoun, learn, wide, children }: AreaShellProps) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { isElectron, trafficLightPadding, dragRegionStyle, noDragRegionStyle } =
    useTitleBarChrome({ collapsedPadding: "8px" });

  // The detail page steps back to the list; the list exits to the app.
  // Both read org-less, so the list's path and the page's agree in either form.
  const isDetail = orgFreePath(pathname).startsWith(`${orgFreePath(areaPath)}/`);
  const onClose = useCallback(() => {
    void navigate(getParentRouteNavigateOptions(pathname));
  }, [navigate, pathname]);

  // Escape leaves, matching SettingsPage. It steps aside for text entry and for
  // any open dialog, which owns Escape for itself — otherwise dismissing the
  // edit form would also navigate away from the page underneath it.
  useEscape(onClose);

  const exitLabel = isDetail ? areaLabel : isElectron ? "Close" : "Back";
  const exitTooltip = isDetail
    ? `Back to ${areaLabel.toLowerCase()} (Esc)`
    : isElectron
      ? `Close ${areaNoun} (Esc)`
      : "Back to app (Esc)";

  return (
    <div className="flex h-screen w-full flex-col bg-background">
      <header
        className={cn(
          "relative z-[100] flex h-12 shrink-0 select-none items-center border-b border-border/60 bg-card dense-ui",
          isElectron && "cursor-move",
        )}
        style={dragRegionStyle}
      >
        <div
          className="flex items-center transition-[padding] duration-200 ease-in-out"
          style={{ paddingLeft: trafficLightPadding }}
        >
          <div className="flex cursor-default items-center" style={noDragRegionStyle}>
            <Tooltip content={exitTooltip} side="bottom" delay={300}>
              <button
                type="button"
                onClick={onClose}
                className="inline-flex h-9 items-center gap-2 rounded-md px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted/70 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={exitTooltip.replace(" (Esc)", "")}
              >
                {isElectron && !isDetail ? (
                  <X className="h-4 w-4" aria-hidden="true" />
                ) : (
                  <ArrowLeft className="h-4 w-4" aria-hidden="true" />
                )}
                <span>{exitLabel}</span>
              </button>
            </Tooltip>
          </div>
        </div>
        <div className="flex-1 self-stretch" style={dragRegionStyle} />
        {learn && (
          <div className="pr-3" style={noDragRegionStyle}>
            <LearnLink section={learn} />
          </div>
        )}
        <div className="max-w-64 pr-3" style={noDragRegionStyle}>
          <OrgSwitcher />
        </div>
      </header>

      {/* `relative`, so an absolutely positioned descendant (every sr-only
          span) is contained by the scroller rather than stretching the
          document — without it the window scrolls past the content. */}
      <PageMain className="relative flex-1 overflow-y-auto">
        <div className={cn("mx-auto w-full py-8", wide ? "max-w-7xl px-4 sm:px-6" : "max-w-5xl px-6")}>
          {/* A crash in the area stays in the area: the title bar above keeps
              the way out, which in Electron is the only one. */}
          <ErrorBoundary layout="panel">{children}</ErrorBoundary>
        </div>
      </PageMain>
    </div>
  );
}
