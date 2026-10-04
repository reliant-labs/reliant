// Copyright (c) 2025 Reliant Labs

/**
 * Chrome for an app area that lives outside the project shell — /automations
 * and /runs today, the merged Workflows area later (WORKFLOW_UI.md §12 Phase
 * 3). A title bar with the way out, and the content below it.
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
import { Tooltip } from "../ui/Tooltip";

interface AreaShellProps {
  /** The area's list path, e.g. "/runs". Anything below it is a detail page. */
  areaPath: string;
  /** What the detail page's exit returns to, e.g. "All runs". */
  areaLabel: string;
  /** Lower-case noun for the list page's exit tooltip, e.g. "runs". */
  areaNoun: string;
  /**
   * "column" (default) is a centred, scrolling reading column for lists and
   * forms. "fill" hands the page the whole remaining height and lets it own
   * scrolling, for a page hosting a transcript.
   */
  layout?: "column" | "fill";
  children: ReactNode;
}

export function AreaShell({ areaPath, areaLabel, areaNoun, layout = "column", children }: AreaShellProps) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const { isElectron, trafficLightPadding, dragRegionStyle, noDragRegionStyle } =
    useTitleBarChrome({ collapsedPadding: "8px" });

  // The detail page steps back to the list; the list exits to the app.
  const isDetail = pathname.startsWith(`${areaPath}/`);
  const onClose = useCallback(() => {
    void navigate(getParentRouteNavigateOptions(pathname));
  }, [navigate, pathname]);

  // Escape leaves, matching SettingsPage. It steps aside for text entry and for
  // any open dialog, which owns Escape for itself — otherwise dismissing the
  // edit form would also navigate away from the page underneath it.
  useEffect(() => {
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
      onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

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
            <Tooltip content={exitTooltip} placement="bottom" delay={300}>
              <button
                type="button"
                onClick={onClose}
                className="inline-flex h-9 items-center gap-2 rounded-md px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted/70 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
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
      </header>

      {layout === "fill" ? (
        <main className="flex min-h-0 flex-1 flex-col">{children}</main>
      ) : (
        <main className="flex-1 overflow-y-auto">
          <div className="mx-auto w-full max-w-5xl px-6 py-8">{children}</div>
        </main>
      )}
    </div>
  );
}
