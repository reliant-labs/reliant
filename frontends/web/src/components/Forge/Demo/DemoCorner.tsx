// Copyright (c) 2025 Reliant Labs

/**
 * The demo's bottom-right corner: one fixed stack the tour toast and the
 * view tips share, so they sit one above the other instead of on top of each
 * other, whichever mounts first. The stack itself takes no clicks; its cards do.
 */

import type { ReactNode } from "react";
import { createPortal } from "react-dom";

const ID = "forge-demo-corner";

function corner(): HTMLElement {
  let el = document.getElementById(ID);
  if (!el) {
    el = document.createElement("div");
    el.id = ID;
    el.className = "pointer-events-none fixed bottom-4 right-4 z-[90] flex flex-col items-end gap-3";
    document.body.appendChild(el);
  }
  return el;
}

/** `order`: lower sits higher in the stack (tips 1, the toast 2). */
export function DemoCorner({ order, children }: { order: number; children: ReactNode }) {
  return createPortal(
    <div className="pointer-events-auto w-[min(22rem,calc(100vw-2rem))]" style={{ order }}>
      {children}
    </div>,
    corner(),
  );
}
