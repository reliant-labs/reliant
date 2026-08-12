/**
 * useWorkflowKeyboardShortcuts
 *
 * Owns the two keyboard listener effects in WorkflowBuilder:
 *   1. Undo / Redo: Ctrl/Cmd+Z, Ctrl/Cmd+Shift+Z, Ctrl/Cmd+Y on the `window`.
 *   2. Escape: capture-phase Escape on the `window` that drives the workflow
 *      navigation stack — exit inline-edit, then deselect/close panels, then
 *      fall back to the global "go back to hub" handler.
 *
 * Extracted from `WorkflowBuilder.tsx`. Behavior is intentionally identical
 * to the original effects — same listeners, same options (capture phase for
 * Escape), same dependency arrays. The hook is a wrapper: it owns no state.
 *
 * The Escape handler intentionally returns early when:
 *   - the target is an input/textarea/contenteditable;
 *   - any of the modal flags are open;
 *   - the URL has `?tour=…` (defer to the onboarding tour).
 *
 * SCOPE. These listeners are safe to keep outside the central shortcut
 * dispatcher because the workflow builder lives on its own route (`/workflow/*`)
 * and ModernApp — which mounts the dispatcher — only renders on `/` and
 * `/project/$projectId`. There is no global handler here to race, and the
 * `stopImmediatePropagation` below is defensive rather than load-bearing.
 *
 * If the dispatcher is ever mounted app-wide, these must move into the
 * `workflow-canvas` context instead: an inner-context Escape shadows the global
 * one by precedence, which gets the same result without the race.
 */

import { useEffect } from "react";

export interface UseWorkflowKeyboardShortcutsArgs {
  onUndo: () => void;
  onRedo: () => void;
  /** Called when Escape lands without any deselect/close action to take. */
  onEscape: () => void;
  /** Whether we're inside an inline-edit (loop/workflow body). */
  isEditingLoop: boolean;
  /** Exit the current inline-edit. Receives `saveChanges` flag. */
  exitLoopEdit: (saveChanges?: boolean) => void;
  /** Whether the workflow is a builtin (affects exit save behavior). */
  isBuiltinWorkflow: boolean;
  /** Selection state — Escape closes these before falling back to onEscape. */
  hasSelectedNode: boolean;
  hasSelectedEdge: boolean;
  showSettingsEditor: boolean;
  setSelectedNodeId: (id: string | null) => void;
  setSelectedEdgeId: (id: string | null) => void;
  setShowSettingsEditor: (open: boolean) => void;
  /** Modal flags — Escape defers to modals when they're open. */
  showTemplateModal: boolean;
  showExitConfirmModal: boolean;
  showActiveChatModal: boolean;
}

export function useWorkflowKeyboardShortcuts({
  onUndo,
  onRedo,
  onEscape,
  isEditingLoop,
  exitLoopEdit,
  isBuiltinWorkflow,
  hasSelectedNode,
  hasSelectedEdge,
  showSettingsEditor,
  setSelectedNodeId,
  setSelectedEdgeId,
  setShowSettingsEditor,
  showTemplateModal,
  showExitConfirmModal,
  showActiveChatModal,
}: UseWorkflowKeyboardShortcutsArgs): void {
  // Keyboard shortcuts for undo/redo
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Ctrl/Cmd + Z for undo
      if ((e.ctrlKey || e.metaKey) && e.key === "z" && !e.shiftKey) {
        e.preventDefault();
        onUndo();
      }
      // Ctrl/Cmd + Shift + Z for redo
      else if ((e.ctrlKey || e.metaKey) && e.key === "z" && e.shiftKey) {
        e.preventDefault();
        onRedo();
      }
      // Ctrl/Cmd + Y for redo (alternative)
      else if ((e.ctrlKey || e.metaKey) && e.key === "y") {
        e.preventDefault();
        onRedo();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [onUndo, onRedo]);

  // Escape key handling - deselect first, then navigate back to hub
  // Uses capture phase to intercept before ModernApp's global handler
  useEffect(() => {
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;

      // Skip if we're in an input field
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.contentEditable === "true"
      ) {
        return;
      }

      // Skip if modals are open - let them handle ESC
      if (showTemplateModal || showExitConfirmModal || showActiveChatModal) {
        return;
      }

      // Defer to onboarding tour if it's active. Tour activity lives in the
      // URL (`?tour=<step>`); read it directly since this handler is outside
      // the React render cycle.
      if (new URLSearchParams(window.location.search).has("tour")) return;

      // Prevent ModernApp from handling this ESC
      e.preventDefault();
      e.stopPropagation();
      e.stopImmediatePropagation();

      // Priority 1: If editing a loop, exit loop edit (save changes only for non-builtins)
      if (isEditingLoop) {
        exitLoopEdit(!isBuiltinWorkflow);
        return;
      }

      // Priority 2: If something is selected or settings panel is open, deselect/close
      if (hasSelectedNode || hasSelectedEdge || showSettingsEditor) {
        setSelectedNodeId(null);
        setSelectedEdgeId(null);
        setShowSettingsEditor(false);
        return;
      }

      // Priority 3: Nothing selected - go back to hub
      onEscape();
    };

    // Use window with capture phase to run BEFORE document-level handlers (like ModernApp's global shortcuts)
    // This ensures the workflow navigation stack is respected (panels → hub → exit)
    window.addEventListener("keydown", handleEscape, true);
    return () => window.removeEventListener("keydown", handleEscape, true);
  }, [
    isEditingLoop,
    exitLoopEdit,
    hasSelectedNode,
    hasSelectedEdge,
    showSettingsEditor,
    showTemplateModal,
    showExitConfirmModal,
    showActiveChatModal,
    onEscape,
    isBuiltinWorkflow,
    setSelectedNodeId,
    setSelectedEdgeId,
    setShowSettingsEditor,
  ]);
}
