/**
 * Mounts the single keyboard dispatcher.
 *
 * One capture-phase listener at the document root serves every shortcut. The
 * handler map is held in a ref so re-renders never re-register the listener,
 * and the registry comes from the shortcuts store so a user remap takes effect
 * immediately without a reload.
 *
 * It is the same path on both surfaces. In a browser tab the keystroke is the
 * only way in. In the desktop app the native menu also delivers a few of these
 * shortcuts (its accelerators and its clicks); those are routed into the same
 * dispatcher by id, so a menu action runs the very handler the keystroke runs
 * and a shortcut fires once whichever of the two saw it.
 */

import { useEffect, useRef, useState } from "react";
import { logger } from "../lib/logger";
import { createDispatcher, type Dispatcher } from "../lib/keyboard/dispatcher";
import { defaultShortcuts, useShortcutsStore } from "../store/shortcutsStore";

export type ShortcutHandlers = Record<string, (() => void) | undefined>;

export interface UseAppKeyboardShortcutsResult {
  /** Armed sequence prefix ("meta+K"), or "" — drives the on-screen hint. */
  pendingSequence: string;
}

type MenuSubscribe = (callback: () => void) => (() => void) | void;

/**
 * Native menu items that deliver a shortcut, by the preload subscriber for the
 * IPC channel the item sends, mapped to the shortcut id it means.
 *
 * Declared here, where it is consumed, rather than as a dependency on the full
 * electronAPI type: this hook needs nothing from it but these subscribers.
 */
export const MENU_DELIVERED_SHORTCUTS: ReadonlyArray<
  readonly [subscriber: string, shortcutId: string]
> = [
  ["onCreateNewTab", "newChat"],
  ["onCloseCurrentTab", "closeTab"],
  ["onReopenLastTab", "reopenLastClosedFile"],
  ["onToggleTerminal", "toggleTerminal"],
  ["onNewTerminal", "newTerminal"],
];

function menuSubscriber(name: string): MenuSubscribe | undefined {
  if (typeof window === "undefined" || !window.electronAPI) return undefined;
  const candidate = (window.electronAPI as unknown as Record<string, unknown>)[
    name
  ];
  return typeof candidate === "function"
    ? (candidate as MenuSubscribe)
    : undefined;
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
      getHandler: (name) => handlersRef.current[name],
      onPendingChange: setPendingSequence,
      onError: (error, shortcutId) => {
        logger.error(`Shortcut handler failed: ${shortcutId}`, error);
      },
    });
    dispatcherRef.current = dispatcher;

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
      if (dispatcherRef.current === dispatcher) dispatcherRef.current = null;
      dispatcher.destroy();
    };
  }, [registry]);

  // Desktop only: the native menu's way in. Subscribed once — the dispatcher
  // is read through the ref, so a registry rebuild does not resubscribe.
  useEffect(() => {
    const cleanups: Array<() => void> = [];

    for (const [subscriber, shortcutId] of MENU_DELIVERED_SHORTCUTS) {
      const subscribe = menuSubscriber(subscriber);
      if (!subscribe) continue;

      const unsubscribe = subscribe(() => {
        const dispatcher = dispatcherRef.current;
        if (dispatcher) {
          dispatcher.invoke(shortcutId);
          return;
        }
        // Shortcuts are still loading — the first moments after launch, which
        // is exactly when the tray's "New Chat" arrives. No keystroke can have
        // fired yet, so there is nothing to echo: run the handler directly.
        const handlerName = defaultShortcuts[shortcutId]?.handler;
        const handler = handlerName ? handlersRef.current[handlerName] : undefined;
        try {
          handler?.();
        } catch (error) {
          logger.error(`Shortcut handler failed: ${shortcutId}`, error);
        }
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    return () => cleanups.forEach((cleanup) => cleanup());
  }, []);

  return { pendingSequence };
}
