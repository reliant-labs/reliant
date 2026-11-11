// Copyright (c) 2025 Reliant Labs

/**
 * The demo's corner: one fixed stack the tour toast and the page cards share,
 * so they sit one above the other instead of on top of each other, whichever
 * mounts first. The stack itself takes no taps; its cards do.
 *
 * On a phone it is a full-width stack at the bottom (a bottom sheet), and it
 * steps aside while the console's nav drawer is open (forge-ui SidebarLayout:
 * a portalled modal aside), so it never sits over the drawer's rows.
 */

import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

const ID = "forge-demo-corner";

function corner(): HTMLElement {
  let el = document.getElementById(ID);
  if (!el) {
    el = document.createElement("div");
    el.id = ID;
    el.className =
      "pointer-events-none fixed inset-x-2 bottom-2 z-[90] flex flex-col items-stretch gap-2 sm:inset-x-auto sm:bottom-4 sm:right-4 sm:items-end sm:gap-3";
    document.body.appendChild(el);
  }
  return el;
}

// SidebarLayout portals a uniquely identified aside on narrow screens. Observe
// its semantic modal state, not the historical fixed id or its former location.
const drawerOpen = () => {
  for (const trigger of document.querySelectorAll("button[aria-controls][aria-expanded='true']")) {
    const panel = document.getElementById(trigger.getAttribute("aria-controls")!);
    if (panel?.matches("aside[role='dialog'][aria-modal='true']") && panel.querySelector("nav")) return true;
  }
  return false;
};

/** Whether the console's nav drawer is open (phones). */
export function useConsoleDrawerOpen(): boolean {
  const [open, setOpen] = useState(drawerOpen);
  useEffect(() => {
    const observer = new MutationObserver(() => setOpen(drawerOpen()));
    observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["aria-modal", "aria-expanded", "aria-controls"] });
    return () => observer.disconnect();
  }, []);
  return open;
}

/** `order`: lower sits higher in the stack (page cards 1, the toast 2). */
export function DemoCorner({ order, children }: { order: number; children: ReactNode }) {
  const hidden = useConsoleDrawerOpen();
  if (hidden) return null;
  return createPortal(
    <div className="pointer-events-auto w-full sm:w-[min(22rem,calc(100vw-2rem))]" style={{ order }}>
      {children}
    </div>,
    corner(),
  );
}
