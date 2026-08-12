/**
 * The single keyboard dispatcher.
 *
 * One capture-phase listener at the document root owns every registered
 * shortcut. Components no longer attach their own window/document listeners and
 * race each other by mount order — they register into the context registry and
 * the dispatcher decides who wins by context precedence.
 *
 * Leaf text-entry behavior (Enter to send, Shift+Enter for newline, xterm's own
 * key handling) still belongs to the component: those are not shortcuts, they
 * are the semantics of the control. The dispatcher stays out of their way by
 * only claiming chords that are actually registered.
 */

import { eventToChordString } from "./chord";
import { detectActiveContexts, type ShortcutContext } from "./contexts";
import type { ShortcutRegistry } from "./registry";

/** How long a sequence prefix stays armed before it expires. */
export const SEQUENCE_TIMEOUT_MS = 1500;

export interface DispatcherOptions {
  registry: ShortcutRegistry;
  /** Look up the live handler for a shortcut's handler name. */
  getHandler: (handlerName: string) => (() => void) | undefined;
  /** Override context detection (tests). */
  detectContexts?: (target: Element | null) => ShortcutContext[];
  /** Notified when a sequence prefix is armed or cleared, for the hint UI. */
  onPendingChange?: (pending: string) => void;
  onError?: (error: unknown, shortcutId: string) => void;
}

export interface Dispatcher {
  handleKeyDown: (event: KeyboardEvent) => void;
  /** Currently armed sequence prefix, or "" when idle. */
  getPending: () => string;
  clearPending: () => void;
  destroy: () => void;
}

export function createDispatcher(options: DispatcherOptions): Dispatcher {
  const {
    registry,
    getHandler,
    detectContexts = detectActiveContexts,
    onPendingChange,
    onError,
  } = options;

  let pending = "";
  let pendingTimer: ReturnType<typeof setTimeout> | undefined;

  const setPending = (next: string) => {
    if (pending === next) return;
    pending = next;
    onPendingChange?.(next);
  };

  const clearPending = () => {
    if (pendingTimer !== undefined) {
      clearTimeout(pendingTimer);
      pendingTimer = undefined;
    }
    setPending("");
  };

  const armPending = (prefix: string) => {
    if (pendingTimer !== undefined) clearTimeout(pendingTimer);
    setPending(prefix);
    pendingTimer = setTimeout(clearPending, SEQUENCE_TIMEOUT_MS);
  };

  const handleKeyDown = (event: KeyboardEvent) => {
    // Ignore synthetic repeats from a held key: a shortcut should fire once.
    if (event.repeat) return;

    const chord = eventToChordString(event);
    // Modifier-only press — keep any armed sequence alive.
    if (chord === null) return;

    const target = (event.target as Element | null) ?? null;
    const contexts = detectContexts(target);

    const result = registry.resolve(chord, contexts, pending);

    if (result.kind === "sequence-prefix") {
      // Claim the prefix so it does not leak to the browser or the page.
      event.preventDefault();
      event.stopPropagation();
      armPending(chord);
      return;
    }

    if (result.kind === "none") {
      // An armed sequence that does not complete is abandoned, and the key is
      // left alone so the user's actual keystroke still reaches the page.
      if (pending) clearPending();
      return;
    }

    const shortcut = result.shortcut!;
    clearPending();

    const handler = getHandler(shortcut.handler);
    if (!handler) return;

    if (!shortcut.passthrough) {
      event.preventDefault();
      event.stopPropagation();
      // Nothing downstream should also act on a claimed chord.
      event.stopImmediatePropagation();
    }

    try {
      handler();
    } catch (error) {
      onError?.(error, shortcut.id);
    }
  };

  return {
    handleKeyDown,
    getPending: () => pending,
    clearPending,
    destroy: clearPending,
  };
}
