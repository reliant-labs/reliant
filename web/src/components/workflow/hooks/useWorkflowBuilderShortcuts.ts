/**
 * The builder's configurable shortcuts (config/shortcuts.yaml, context
 * `workflow-canvas`).
 *
 * Contributed to the app's one keyboard host as a layer for as long as the
 * caller is mounted — the builder used to mount dispatchers of its own,
 * because the app-wide one lived in ModernApp and never ran on /workflow/*.
 * Now that it runs on every authenticated route, a second one would see every
 * keystroke too. Same registry either way: one source of truth for bindings,
 * user remaps included, and the shortcut shows in Settings like any other.
 * A handler left undefined is simply absent, so the chord passes through and
 * the builder's own listeners (undo, Escape) are unaffected.
 *
 * The builder's root carries data-context="workflow-canvas", which is what
 * makes these bindings — and only these — active there.
 */

import { parseBinding } from "../../../lib/keyboard/chord";
import { formatBinding, detectPlatform } from "../../../lib/keyboard/platform";
import { useShortcutsStore } from "../../../store/shortcutsStore";
import { useShortcutLayer } from "../../../hooks/useShortcutHost";

export interface WorkflowBuilderShortcutHandlers {
  onOpenStepPalette?: () => void;
  /** Draw an edge between the two selected steps (canvas/SelectionActions). */
  onConnectSelectedSteps?: () => void;
}

export function useWorkflowBuilderShortcuts(handlers: WorkflowBuilderShortcutHandlers): void {
  useShortcutLayer({ handlers: { ...handlers } });
}

/** A builder shortcut as shown to the user ("⌘I"), or "" before shortcuts load. */
export function useBuilderShortcutLabel(id: string): string {
  const binding = useShortcutsStore((state) => (state.shortcuts[id] ? state.getEffectiveBinding(id) : ""));
  if (!binding) return "";
  const { isMac } = detectPlatform();
  return formatBinding(parseBinding(binding, isMac), isMac);
}

/** The palette shortcut as shown to the user ("⌘I"), or "" before shortcuts load. */
export function useStepPaletteShortcutLabel(): string {
  return useBuilderShortcutLabel("openStepPalette");
}
