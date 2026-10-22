/**
 * An open popover, dropdown or menu that Escape should close FIRST.
 *
 * Surfaces that close things on Escape — the workflow builder closes its side
 * panel — must leave Escape to whatever is open on top of them, or a single
 * press closes both (or worse, the page behind). They recognise such a layer
 * by role (dialogs, menus, listboxes) or by `data-escape-layer`, which this
 * hook puts on a popup that has neither: spread the returned props on the
 * popup's root, and Escape closes it.
 */
import { useEffect } from "react";

export const ESCAPE_LAYER_ATTRIBUTE = "data-escape-layer";

/** Elements that own Escape while they are open. */
export const ESCAPE_LAYER_SELECTOR = [
  '[aria-modal="true"]',
  '[role="dialog"]',
  '[role="alertdialog"]',
  '[role="menu"]',
  '[role="listbox"]',
  '[data-dropdown-open="true"]',
  `[${ESCAPE_LAYER_ATTRIBUTE}]`,
].join(", ");

/** Whether an overlay that handles its own Escape is open. */
export function hasOpenEscapeLayer(root: ParentNode = document): boolean {
  return root.querySelector(ESCAPE_LAYER_SELECTOR) !== null;
}

export function useEscapeLayer(open: boolean, close: () => void): { [ESCAPE_LAYER_ATTRIBUTE]: "" } {
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      event.preventDefault();
      close();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, close]);
  return { [ESCAPE_LAYER_ATTRIBUTE]: "" };
}
