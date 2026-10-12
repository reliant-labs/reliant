/**
 * Mounts the single keyboard dispatcher.
 *
 * Called once, by the keyboard host (components/Shortcuts/ShortcutHost.tsx),
 * which decides where it runs — every authenticated desktop route — and where
 * each handler comes from. Do not call it anywhere else: a second dispatcher
 * would see every keystroke too, and both would fire.
 *
 * One capture-phase listener at the document root serves every shortcut. The
 * handler lookup is held in a ref so re-renders never re-register the
 * listener, and the registry comes from the shortcuts store so a user remap
 * takes effect immediately without a reload.
 *
 * It is the same path on both surfaces. In a browser tab the keystroke is the
 * only way in. In the desktop app the native menu delivers shortcuts too — its
 * accelerators and its clicks — on one IPC channel carrying the shortcut id;
 * those are routed into the same dispatcher, so a menu action runs the very
 * handler the keystroke runs and a shortcut fires once whichever of the two
 * saw it.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { logger } from "../lib/logger";
import { createDispatcher, type Dispatcher } from "../lib/keyboard/dispatcher";
import { defaultShortcuts, useShortcutsStore } from "../store/shortcutsStore";

/** The live handler for a handler name, or undefined where it does not run. */
export type ShortcutHandlerLookup = (
  handlerName: string,
) => (() => void) | undefined;

export interface UseAppKeyboardShortcutsOptions {
  /**
   * While true, keystrokes and menu deliveries are left alone entirely — the
   * shortcut remap box is recording the raw chord.
   */
  isSuspended?: () => boolean;
}

export interface UseAppKeyboardShortcutsResult {
  /** Armed sequence prefix ("meta+K"), or "" — drives the on-screen hint. */
  pendingSequence: string;
  /** Abandon an armed sequence, as unrelated input would. */
  clearPendingSequence: () => void;
}

export function useAppKeyboardShortcuts(
  getHandler: ShortcutHandlerLookup,
  options: UseAppKeyboardShortcutsOptions = {},
): UseAppKeyboardShortcutsResult {
  const registry = useShortcutsStore((state) => state.registry);
  const initializeShortcuts = useShortcutsStore(
    (state) => state.initializeShortcuts,
  );

  const [pendingSequence, setPendingSequence] = useState("");

  // The lookup may change identity on any render; a ref keeps the listener
  // stable so shortcuts never miss a keystroke during a re-render.
  const getHandlerRef = useRef(getHandler);
  getHandlerRef.current = getHandler;
  const isSuspendedRef = useRef(options.isSuspended);
  isSuspendedRef.current = options.isSuspended;

  // The live dispatcher, for menu deliveries that arrive between registry
  // rebuilds. Null until shortcuts have loaded.
  const dispatcherRef = useRef<Dispatcher | null>(null);

  useEffect(() => {
    void initializeShortcuts();
  }, [initializeShortcuts]);

  useEffect(() => {
    if (registry.size === 0) return;

    const dispatcher = createDispatcher({
      registry,
      getHandler: (name) => getHandlerRef.current(name),
      onPendingChange: setPendingSequence,
      onError: (error, shortcutId) => {
        logger.error(`Shortcut handler failed: ${shortcutId}`, error);
      },
    });
    dispatcherRef.current = dispatcher;

    // Capture phase at the document root: the dispatcher decides ownership
    // before any component sees the event, but it only claims chords that are
    // actually registered, so ordinary typing and leaf controls are untouched.
    const onKeyDown = (event: KeyboardEvent) => {
      if (isSuspendedRef.current?.()) return;
      dispatcher.handleKeyDown(event);
    };
    document.addEventListener("keydown", onKeyDown, true);

    // A sequence left armed while the window is unfocused would fire on the
    // user's next unrelated keystroke.
    const onBlur = () => dispatcher.clearPending();
    window.addEventListener("blur", onBlur);

    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      window.removeEventListener("blur", onBlur);
      if (dispatcherRef.current === dispatcher) dispatcherRef.current = null;
      dispatcher.destroy();
    };
  }, [registry]);

  // Desktop only: the native menu's way in. Subscribed once — the dispatcher
  // is read through the ref, so a registry rebuild does not resubscribe.
  useEffect(() => {
    const api = typeof window === "undefined" ? undefined : window.electronAPI;
    if (typeof api?.onMenuShortcut !== "function") return;

    const unsubscribe = api.onMenuShortcut((shortcutId) => {
      if (isSuspendedRef.current?.()) return;

      const dispatcher = dispatcherRef.current;
      if (dispatcher) {
        // False for the echo of a keystroke already handled — and for an id
        // with nothing to run here, which should not happen: every shortcut
        // has a reach on every route the host runs on (lib/keyboard/scope).
        if (!dispatcher.invoke(shortcutId)) {
          logger.debug(`Menu shortcut not run: ${shortcutId}`);
        }
        return;
      }
      // Shortcuts are still loading — the first moments after launch, which
      // is exactly when the tray's "New Chat" arrives. No keystroke can have
      // fired yet, so there is nothing to echo: run the handler directly.
      const handlerName = defaultShortcuts[shortcutId]?.handler;
      const handler = handlerName ? getHandlerRef.current(handlerName) : undefined;
      if (!handler) {
        logger.warn(`Menu shortcut has no handler: ${shortcutId}`);
        return;
      }
      try {
        handler();
      } catch (error) {
        logger.error(`Shortcut handler failed: ${shortcutId}`, error);
      }
    });

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, []);

  const clearPendingSequence = useCallback(() => {
    dispatcherRef.current?.clearPending();
  }, []);

  return { pendingSequence, clearPendingSequence };
}
