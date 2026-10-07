/**
 * useWorkflowKeyboardShortcuts
 *
 * Owns the builder's two window-level keyboard listeners:
 *   1. Undo / Redo: Ctrl/Cmd+Z, Ctrl/Cmd+Shift+Z, Ctrl/Cmd+Y.
 *   2. Escape: closes the open side panels (a selected step or edge, the
 *      start panel, a trigger's editor, Inputs; then the Test run panel).
 *
 * ESCAPE NEVER NAVIGATES. It used to fall back to "leave the builder" when
 * nothing was selected, and it could not see popovers, menus or the step
 * palette, so pressing Escape to close a popover threw the user out to the
 * Library (research/WORKFLOW_EDITOR_UX_REVIEW.md issue 10). It no longer exits
 * a loop body either: that applies the body's edits, and Apply / Discard /
 * Back are the buttons for it. Leaving is the Back button, behind the
 * unsaved-changes guard.
 *
 * An open overlay owns its own Escape. The handler runs in the CAPTURE phase,
 * before any overlay's listener, and only to ASK whether one is open — if so
 * it does nothing and lets the overlay close itself. Checking in the capture
 * phase matters: by the bubble phase an overlay may already have closed, and
 * Escape would close a panel behind it too. Overlays are recognized by role
 * (dialogs, menus, listboxes — Modal, the step palette, row menus,
 * dropdowns) or by `data-escape-layer`, which a non-modal popover sets.
 *
 * SCOPE. These listeners are safe outside the central shortcut dispatcher
 * because the builder lives on its own route (`/workflow/*`) and ModernApp,
 * which mounts the dispatcher, does not render there.
 */

import { useEffect } from "react";

import { hasOpenEscapeLayer } from "../../../hooks/useEscapeLayer";

function isTextEntry(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.tagName === "INPUT" ||
    target.tagName === "TEXTAREA" ||
    target.tagName === "SELECT" ||
    target.isContentEditable
  );
}

export interface UseWorkflowKeyboardShortcutsArgs {
  onUndo: () => void;
  onRedo: () => void;
  /** Whether any side panel Escape should close is open. */
  hasOpenPanel: boolean;
  /** Close every side panel (selection, start panel, trigger editor, Inputs). */
  closePanels: () => void;
  /** Whether the Test run panel is open; closed after the side panels. */
  hasTestRunPanel: boolean;
  closeTestRunPanel: () => void;
}

export function useWorkflowKeyboardShortcuts({
  onUndo,
  onRedo,
  hasOpenPanel,
  closePanels,
  hasTestRunPanel,
  closeTestRunPanel,
}: UseWorkflowKeyboardShortcutsArgs): void {
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "z" && !e.shiftKey) {
        e.preventDefault();
        onUndo();
      } else if ((e.ctrlKey || e.metaKey) && e.key === "z" && e.shiftKey) {
        e.preventDefault();
        onRedo();
      } else if ((e.ctrlKey || e.metaKey) && e.key === "y") {
        e.preventDefault();
        onRedo();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [onUndo, onRedo]);

  useEffect(() => {
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      // A field (or Monaco's textarea) handles its own Escape.
      if (isTextEntry(e.target)) return;
      // The innermost overlay closes itself.
      if (hasOpenEscapeLayer()) return;
      // The onboarding tour owns Escape while it runs (`?tour=<step>`).
      if (new URLSearchParams(window.location.search).has("tour")) return;

      if (hasOpenPanel) {
        e.preventDefault();
        closePanels();
        return;
      }
      if (hasTestRunPanel) {
        e.preventDefault();
        closeTestRunPanel();
      }
      // Nothing open: Escape does nothing. It never leaves the builder.
    };

    window.addEventListener("keydown", handleEscape, true);
    return () => window.removeEventListener("keydown", handleEscape, true);
  }, [hasOpenPanel, closePanels, hasTestRunPanel, closeTestRunPanel]);
}
