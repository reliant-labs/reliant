/**
 * Mounts the single keyboard dispatcher.
 *
 * One capture-phase listener at the document root serves every shortcut. The
 * handler map is held in a ref so re-renders never re-register the listener,
 * and the registry comes from the shortcuts store so a user remap takes effect
 * immediately without a reload.
 */

import { useEffect, useRef, useState } from "react";
import { logger } from "../lib/logger";
import { createDispatcher } from "../lib/keyboard/dispatcher";
import { useShortcutsStore } from "../store/shortcutsStore";

export type ShortcutHandlers = Record<string, (() => void) | undefined>;

export interface UseAppKeyboardShortcutsResult {
  /** Armed sequence prefix ("meta+K"), or "" — drives the on-screen hint. */
  pendingSequence: string;
}

export function useAppKeyboardShortcuts(
  handlers: ShortcutHandlers,
): UseAppKeyboardShortcutsResult {
  const registry = useShortcutsStore((state) => state.registry);
  const initializeShortcuts = useShortcutsStore(
    (state) => state.initializeShortcuts,
  );

  const [pendingSequence, setPendingSequence] = useState("");

  // Handlers change identity on nearly every render; a ref keeps the listener
  // stable so shortcuts never miss a keystroke during a re-render.
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;

  useEffect(() => {
    void initializeShortcuts();
  }, [initializeShortcuts]);

  useEffect(() => {
    if (registry.size === 0) return;

    const dispatcher = createDispatcher({
      registry,
      getHandler: (name) => handlersRef.current[name],
      onPendingChange: setPendingSequence,
      onError: (error, shortcutId) => {
        logger.error(`Shortcut handler failed: ${shortcutId}`, error);
      },
    });

    // Capture phase at the document root: the dispatcher decides ownership
    // before any component sees the event, but it only claims chords that are
    // actually registered, so ordinary typing and leaf controls are untouched.
    const onKeyDown = (event: KeyboardEvent) => dispatcher.handleKeyDown(event);
    document.addEventListener("keydown", onKeyDown, true);

    // A sequence left armed while the window is unfocused would fire on the
    // user's next unrelated keystroke.
    const onBlur = () => dispatcher.clearPending();
    window.addEventListener("blur", onBlur);

    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      window.removeEventListener("blur", onBlur);
      dispatcher.destroy();
    };
  }, [registry]);

  return { pendingSequence };
}
