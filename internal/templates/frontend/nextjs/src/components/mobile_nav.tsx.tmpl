"use client";

// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// The navigation, as a phone should present it.
//
// A 240px rail permanently occupying two thirds of a 390px screen is not a
// small version of a sidebar, it is a broken one. Below `md` the same nav
// becomes an overlay that is opened from a hamburger in the header and shut
// by the backdrop, Escape, or navigating — the three ways a person expects to
// dismiss a sheet. At `md` and up this component renders nothing and the
// static rail is unchanged.
//
// The nav markup itself is NOT duplicated: <Nav /> renders inside the drawer,
// so a link added to it appears in both presentations and the two cannot
// drift apart.

import { X } from "lucide-react";
import { usePathname } from "next/navigation";
import { useEffect, useRef } from "react";

import { Nav } from "@/components/nav";
import { useUiStore } from "@/stores/ui-store";

export function MobileNav() {
  const open = useUiStore((s) => s.mobileNavOpen);
  const setOpen = useUiStore((s) => s.setMobileNavOpen);
  const pathname = usePathname();
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);

  // Navigating is what the drawer is FOR, so a tap that changes the route
  // dismisses it. Without this the panel stays over the page it just opened.
  useEffect(() => {
    setOpen(false);
  }, [pathname, setOpen]);

  // Escape closes, and focus moves into the panel so the next Tab lands
  // inside the thing that just appeared rather than continuing down the page
  // behind it.
  useEffect(() => {
    if (!open) return;

    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("keydown", onKey);
    closeRef.current?.focus();

    // The page behind must not scroll under the overlay — on iOS especially,
    // where the scroll chains to the body and the drawer appears to drift.
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [open, setOpen]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 md:hidden">
      {/*
        The backdrop is a button, not a div with onClick: tapping outside to
        dismiss is a real action, and a screen reader should be able to reach
        it rather than being trapped in a sheet with no announced way out.
      */}
      <button
        type="button"
        aria-label="Close navigation"
        onClick={() => setOpen(false)}
        className="absolute inset-0 bg-ink/40 backdrop-blur-[1px]"
      />

      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="Navigation"
        className="absolute inset-y-0 left-0 flex w-[17rem] max-w-[85vw] flex-col bg-surface shadow-lg"
      >
        <div className="flex justify-end p-2">
          <button
            ref={closeRef}
            type="button"
            onClick={() => setOpen(false)}
            aria-label="Close navigation"
            className="rounded-md p-2 text-ink-muted transition hover:bg-surface-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          >
            <X className="h-5 w-5" aria-hidden />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <Nav variant="drawer" />
        </div>
      </div>
    </div>
  );
}
