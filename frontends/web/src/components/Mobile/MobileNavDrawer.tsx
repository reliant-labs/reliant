/**
 * Left-slide navigation drawer for the mobile surface.
 *
 * Replaces the bottom tab bar (see `MobileTabBar`, now deleted): the mobile
 * surface has grown past the three destinations a tab bar can hold, and the
 * chat list — not any of the other destinations — is where a chat-centric
 * app's users actually want to land. A drawer keeps the chat list as the
 * default view with its full vertical height, mirrors the desktop sidebar
 * users already know (`Layout/Sidebar.tsx`), and scales past five items
 * without redesigning the chrome again.
 *
 * Rendered as a portal so it can sit above `MobileShell`'s `overflow-hidden`
 * flex column regardless of which screen is mounted below it.
 *
 * Its destinations are the ONE nav model (U6e, owner: drilling in on a phone
 * brought back an old nav): the hybrid config the desktop nav draws, drill-ins
 * included (Layout/sidebar/MobileHybridNav). Drilling into Agents or
 * Observability keeps the drawer open on the area's rows; a destination closes it.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { dialogOpener } from "../../lib/dialogOpener";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { FolderOpen, X } from "lucide-react";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";
import { MobileInstallMenuItem } from "./MobileInstallMenuItem";
import { MobileOrgSwitcher } from "./MobileOrgSwitcher";
import { mobileProjectsPath } from "@/lib/paths";
import { MobileHybridNav } from "../Layout/sidebar/MobileHybridNav";
import type { NavigateOptions } from "../Layout/sidebar/NavRenderer";
import { holdNavDrawerOpen, navDrawerHeld } from "../Layout/sidebar/navDrawerHold";

interface MobileNavDrawerProps {
  isOpen: boolean;
  onClose: () => void;
}

// Elements a focus trap should cycle between. Scoped to the drawer panel, not
// the whole document — the scrim and everything behind it must stay
// unreachable by keyboard while the drawer is open.
function focusableElements(container: HTMLElement): HTMLElement[] {
  return Array.from(
    container.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  );
}

export function MobileNavDrawer({ isOpen, onClose }: MobileNavDrawerProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const search = useRouterState({ select: (s) => s.location.search }) as Record<string, unknown>;
  const navigate = useNavigate();
  const currentProject = useProjectStore((s) => s.currentProject);
  const panelRef = useRef<HTMLDivElement>(null);
  // The control that opened the drawer (usually the hamburger button; see
  // `dialogOpener` for why not simply the focused element), so closing it (by
  // Escape, scrim tap, or nav) puts focus back where the user's attention was.
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const [visible, setVisible] = useState(false);
  const previousPathnameRef = useRef(pathname);

  // Mounted a frame before `open` so the slide-in can animate rather than
  // snapping to its open position on first paint.
  useEffect(() => {
    if (!isOpen) return;
    restoreFocusRef.current = dialogOpener();
    const raf = requestAnimationFrame(() => setVisible(true));
    return () => cancelAnimationFrame(raf);
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) {
      setVisible(false);
      return;
    }

    const panel = panelRef.current;
    if (panel) {
      const first = focusableElements(panel)[0];
      first?.focus();
    }

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
        return;
      }
      if (e.key !== "Tab" || e.defaultPrevented || !panel) return;

      // The trap moves focus itself on every Tab rather than only wrapping at
      // the ends: Safari's default Tab order skips links, so letting the
      // browser advance from the second-to-last control would carry focus
      // past a trailing link and out of the drawer.
      const elements = focusableElements(panel);
      if (elements.length === 0) return;
      e.preventDefault();
      const current = elements.indexOf(document.activeElement as HTMLElement);
      const step = e.shiftKey ? -1 : 1;
      const next =
        current === -1
          ? e.shiftKey ? elements.length - 1 : 0
          : (current + step + elements.length) % elements.length;
      elements[next]!.focus();
    };

    document.addEventListener("keydown", handleKeyDown);
    document.body.style.overflow = "hidden";

    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      document.body.style.overflow = "";
      restoreFocusRef.current?.focus();
    };
  }, [isOpen, onClose]);

  // Closes on route change — otherwise the drawer stays open over the
  // destination the user just tapped their way into — unless the change is a
  // drill into (or out of) Agents or Observability, whose rows it then shows.
  useEffect(() => {
    if (previousPathnameRef.current !== pathname) {
      previousPathnameRef.current = pathname;
      if (isOpen && !navDrawerHeld()) onClose();
    }
  }, [pathname, isOpen, onClose]);

  // A destination closes the drawer (even the page already on screen); a drill-in keeps it open.
  const onNavigate = useCallback(
    (href: string, options?: NavigateOptions) => {
      if (options?.drill) holdNavDrawerOpen();
      else onClose();
      void navigate({ href });
    },
    [navigate, onClose],
  );

  if (!isOpen) return null;

  return createPortal(
    <div className="fixed inset-0 z-[9999] flex">
      <div
        className={cn(
          "absolute inset-0 bg-scrim transition-opacity duration-200",
          visible ? "opacity-100" : "opacity-0",
        )}
        onClick={onClose}
        aria-hidden
      />

      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="Navigation"
        className={cn(
          "relative flex h-full w-[82%] max-w-xs flex-col border-r border-border bg-background shadow-2xl transition-transform duration-200",
          visible ? "translate-x-0" : "-translate-x-full",
        )}
        style={{
          paddingTop: "env(safe-area-inset-top)",
          paddingBottom: "env(safe-area-inset-bottom)",
        }}
      >
        <div className="flex min-h-[56px] shrink-0 items-center justify-between px-2 pl-4">
          <span className="text-xl font-semibold tracking-tight">Menu</span>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close menu"
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        {/* The org this tab acts in, and the project it contains, where the
            desktop nav has its switcher. */}
        <div className="shrink-0 space-y-2 px-3 pb-2">
          <MobileOrgSwitcher onSwitched={onClose} />
          <Link
            to={mobileProjectsPath()}
            className="flex min-h-[52px] w-full items-center gap-3 rounded-lg px-3 elevation-1 active:bg-foreground/5"
          >
            <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary-ink">
              <FolderOpen className="h-4 w-4" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="text-xs text-muted-foreground">Project</div>
              <div className="truncate text-sm font-medium text-foreground">{currentProject?.name ?? "Select a project"}</div>
            </div>
          </Link>
        </div>

        {/* The one nav model, drill-ins and all; it scrolls inside itself
            (`overscroll-contain`, so a scroll that reaches its end never
            chains to the page behind the drawer). */}
        <div className="flex min-h-0 flex-1 flex-col overscroll-contain">
          <MobileHybridNav pathname={pathname} search={search} onNavigate={onNavigate} />
        </div>

        <div className="shrink-0 px-3 pb-2">
          <MobileInstallMenuItem />
        </div>
      </div>
    </div>,
    document.body,
  );
}
