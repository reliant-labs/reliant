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
 * Its body is the shell's nav column (U6e, owner: "the mobile left nav, on
 * chats, is the old nav"): the same ShellNavColumn the narrow window's drawer
 * draws, header and all (the org row, Find, the project switcher), with the
 * hybrid config, drill-ins included (Layout/sidebar/MobileHybridNav). Drilling
 * into Agents or Observability keeps the drawer open on the area's rows; a
 * destination closes it.
 *
 * It sits BELOW the column's own popovers (the org menu, the project
 * switcher, z-50, portalled to the body): `z-40`, over the routed screen,
 * which MobileShell isolates so no z-index inside a screen reaches it.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { dialogOpener } from "../../lib/dialogOpener";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { cn } from "../../lib/utils";
import { MobileInstallMenuItem } from "./MobileInstallMenuItem";
import { MobileHybridNav } from "../Layout/sidebar/MobileHybridNav";
import type { NavigateOptions } from "../Layout/sidebar/NavRenderer";
import { holdNavDrawerOpen, navDrawerHeld, releaseNavDrawerHold } from "../Layout/sidebar/navDrawerHold";

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
      // A layer the column opened over the drawer (the org menu, the project
      // switcher) keeps its own Tab order: it is portalled outside the panel.
      const active = document.activeElement as HTMLElement | null;
      const layer = active?.closest?.('[role="menu"], [role="listbox"], [role="dialog"]');
      if (layer && layer !== panel && !panel.contains(layer)) return;

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
      else {
        releaseNavDrawerHold();
        onClose();
      }
      void navigate({ href });
    },
    [navigate, onClose],
  );

  if (!isOpen) return null;

  return createPortal(
    <div className="fixed inset-0 z-40 flex">
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
        {/* The shell's nav column, as the narrow window's drawer draws it; it
            scrolls inside itself (`overscroll-contain`, so a scroll that
            reaches its end never chains to the page behind the drawer). Its
            rows are touch targets here: 44px, not the desktop's 32px row height. */}
        <div className="flex min-h-0 flex-1 flex-col overscroll-contain [--row-h:2.75rem]">
          <MobileHybridNav pathname={pathname} search={search} onNavigate={onNavigate} onClose={onClose} />
        </div>

        <div className="shrink-0 px-3 pb-2">
          <MobileInstallMenuItem />
        </div>

        {/* No visible close control, as in the shell's drawer (the scrim and Escape close it);
            a screen reader, with neither, reaches this one. Last, so opening focuses the org row. */}
        <button type="button" onClick={onClose} className="sr-only">
          Close menu
        </button>
      </div>
    </div>,
    document.body,
  );
}
