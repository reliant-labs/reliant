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
 */

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Link, useRouterState } from "@tanstack/react-router";
import {
  FolderOpen,
  Github,
  MessageSquarePlus,
  Search,
  Server,
  Settings,
  User,
  Workflow,
  X,
} from "lucide-react";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";

interface MobileNavDrawerProps {
  isOpen: boolean;
  onClose: () => void;
}

interface NavItem {
  to: string;
  label: string;
  icon: typeof MessageSquarePlus;
}

const NAV_ITEMS: NavItem[] = [
  { to: "/m/new", label: "New chat", icon: MessageSquarePlus },
  { to: "/m/search", label: "Search", icon: Search },
  { to: "/m/workflows", label: "Workflows", icon: Workflow },
  { to: "/m/daemons", label: "Machines", icon: Server },
  { to: "/m/github", label: "GitHub", icon: Github },
  { to: "/m/settings", label: "Settings", icon: Settings },
  { to: "/m/account", label: "Account", icon: User },
];

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
  const currentProject = useProjectStore((s) => s.currentProject);
  const panelRef = useRef<HTMLDivElement>(null);
  // The element focused right before the drawer opened, so closing it (by
  // Escape, scrim tap, or nav) puts focus back where the user's attention
  // actually was — usually the hamburger button that opened it.
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const [visible, setVisible] = useState(false);
  const previousPathnameRef = useRef(pathname);

  // Mounted a frame before `open` so the slide-in can animate rather than
  // snapping to its open position on first paint.
  useEffect(() => {
    if (!isOpen) return;
    restoreFocusRef.current = document.activeElement as HTMLElement | null;
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
      if (e.key !== "Tab" || !panel) return;

      const elements = focusableElements(panel);
      if (elements.length === 0) return;
      const first = elements[0]!;
      const last = elements[elements.length - 1]!;

      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
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
  // destination the user just tapped their way into.
  useEffect(() => {
    if (previousPathnameRef.current !== pathname) {
      previousPathnameRef.current = pathname;
      if (isOpen) onClose();
    }
  }, [pathname, isOpen, onClose]);

  if (!isOpen) return null;

  return createPortal(
    <div className="fixed inset-0 z-[9999] flex">
      <div
        className={cn(
          "absolute inset-0 bg-black/50 transition-opacity duration-200",
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

        {/* The current project as a card rather than a bordered strip: it is
            the drawer's only stateful row — it reports something as well as
            navigating — and the card is what distinguishes it from the six
            plain destinations beneath. */}
        <div className="shrink-0 px-3 pb-2">
          <Link
            to="/m/projects"
            className="flex min-h-[60px] w-full items-center gap-3 rounded-lg px-3 elevation-1 active:bg-foreground/5"
          >
            <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <FolderOpen className="h-4 w-4" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="text-xs uppercase tracking-wide text-muted-foreground">
                Project
              </div>
              <div className="truncate text-sm font-medium text-foreground">
                {currentProject?.name ?? "Select a project"}
              </div>
            </div>
          </Link>
        </div>

        {/* `overscroll-contain` stops a scroll that reaches the end of the nav
            from chaining to the page behind the drawer. The bottom padding
            matters in landscape, where the viewport is ~390px tall and the
            last item would otherwise sit flush against the edge with no hint
            that the list continues. */}
        <nav
          className="flex-1 space-y-0.5 overflow-y-auto overscroll-contain px-3 py-2 pb-6"
          aria-label="Main"
        >
          {NAV_ITEMS.map(({ to, label, icon: Icon }) => {
            const active = pathname === to || pathname.startsWith(`${to}/`);
            return (
              <Link
                key={to}
                to={to}
                aria-current={active ? "page" : undefined}
                className={cn(
                  "flex min-h-[48px] w-full items-center gap-3 rounded-lg px-3 text-sm",
                  "active:bg-foreground/5",
                  // A filled pill, not just colored text: color alone made the
                  // current destination easy to miss at a glance, and it is
                  // the drawer's only orientation cue.
                  active
                    ? "bg-primary/10 font-medium text-primary"
                    : "text-foreground/85",
                )}
              >
                <Icon className="h-4 w-4 shrink-0" />
                {label}
              </Link>
            );
          })}
        </nav>
      </div>
    </div>,
    document.body,
  );
}
