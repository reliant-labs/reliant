/**
 * The builder's configurable shortcuts (config/shortcuts.yaml, context
 * `workflow-canvas`).
 *
 * The app-wide dispatcher mounts in ModernApp, which never renders on
 * /workflow/*, so the builder mounts its own over the SAME registry: one
 * source of truth for bindings, user remaps included, and the shortcut shows
 * in Settings like any other. It resolves only the handlers passed here; a
 * chord bound to a handler it does not have passes through untouched, so the
 * builder's own listeners (undo, Escape) are unaffected.
 *
 * The builder's root carries data-context="workflow-canvas", which is what
 * makes these bindings — and only these — active there.
 */

import { useEffect, useRef } from "react";

import { createDispatcher } from "../../../lib/keyboard/dispatcher";
import { parseBinding } from "../../../lib/keyboard/chord";
import { formatBinding, detectPlatform } from "../../../lib/keyboard/platform";
import { useShortcutsStore } from "../../../store/shortcutsStore";

export interface WorkflowBuilderShortcutHandlers {
  onOpenStepPalette?: () => void;
}

export function useWorkflowBuilderShortcuts(handlers: WorkflowBuilderShortcutHandlers): void {
  const registry = useShortcutsStore((state) => state.registry);
  const initializeShortcuts = useShortcutsStore((state) => state.initializeShortcuts);
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;

  useEffect(() => {
    void initializeShortcuts();
  }, [initializeShortcuts]);

  useEffect(() => {
    if (registry.size === 0) return;
    const dispatcher = createDispatcher({
      registry,
      getHandler: (name) => handlersRef.current[name as keyof WorkflowBuilderShortcutHandlers],
    });
    const onKeyDown = (event: KeyboardEvent) => dispatcher.handleKeyDown(event);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      dispatcher.destroy();
    };
  }, [registry]);
}

/** The palette shortcut as shown to the user ("⌘I"), or "" before shortcuts load. */
export function useStepPaletteShortcutLabel(): string {
  const binding = useShortcutsStore((state) => (state.shortcuts.openStepPalette ? state.getEffectiveBinding("openStepPalette") : ""));
  if (!binding) return "";
  const { isMac } = detectPlatform();
  return formatBinding(parseBinding(binding, isMac), isMac);
}
