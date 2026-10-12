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
 *
 * It is the one path on every surface. In a browser tab it sees every
 * keystroke. In the desktop app the native menu is a second way in to a few of
 * the same shortcuts (its accelerators and its clicks), and those arrive
 * through `invoke` — so the menu runs the very handler the keystroke would
 * have, and a menu echo of a keystroke already handled here is dropped.
 */

import { eventToChordString } from "./chord";
import { detectActiveContexts, type ShortcutContext } from "./contexts";
import type { ShortcutRegistry } from "./registry";

/** How long a sequence prefix stays armed before it expires. */
export const SEQUENCE_TIMEOUT_MS = 1500;

/**
 * How long after handling a keystroke a menu delivery of the same shortcut is
 * treated as that keystroke's echo rather than a second request.
 *
 * Which of the two sees a menu accelerator first is the platform's call: macOS
 * offers the keydown to the page and only falls back to the menu when the page
 * leaves it unclaimed; Windows and Linux can resolve the accelerator before the
 * page sees anything. An echo, when there is one, follows within milliseconds,
 * while nobody reaches for the menu to repeat a shortcut in half a second.
 */
export const MENU_ECHO_WINDOW_MS = 500;

export interface DispatcherOptions {
  registry: ShortcutRegistry;
  /** Look up the live handler for a shortcut's handler name. */
  getHandler: (handlerName: string) => (() => void) | undefined;
  /** Override context detection (tests). */
  detectContexts?: (target: Element | null) => ShortcutContext[];
  /** Notified when a sequence prefix is armed or cleared, for the hint UI. */
  onPendingChange?: (pending: string) => void;
  onError?: (error: unknown, shortcutId: string) => void;
  /** Clock for echo detection (tests). */
  now?: () => number;
}

export interface Dispatcher {
  handleKeyDown: (event: KeyboardEvent) => void;
  /**
   * Run a shortcut by id for a delivery that was not a keystroke this
   * dispatcher saw — the desktop app's native menu. Returns whether it ran:
   * false for an unknown id, a missing handler, or the echo of a keystroke
   * that already fired it.
   */
  invoke: (shortcutId: string) => boolean;
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
    now = Date.now,
  } = options;

  let pending = "";
  let pendingTimer: ReturnType<typeof setTimeout> | undefined;
  /** The last shortcut a keystroke fired, for menu echo detection. */
  let lastKeyFire: { id: string; at: number } | undefined;

  const run = (handler: () => void, shortcutId: string) => {
    try {
      handler();
    } catch (error) {
      onError?.(error, shortcutId);
    }
  };

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

    lastKeyFire = { id: shortcut.id, at: now() };
    run(handler, shortcut.id);
  };

  const invoke = (shortcutId: string): boolean => {
    const shortcut = registry.get(shortcutId);
    if (!shortcut) return false;

    if (
      lastKeyFire?.id === shortcutId &&
      now() - lastKeyFire.at < MENU_ECHO_WINDOW_MS
    ) {
      // The menu saw the keystroke this dispatcher just handled. Consumed, so
      // a deliberate menu click right after still runs.
      lastKeyFire = undefined;
      return false;
    }

    const handler = getHandler(shortcut.handler);
    if (!handler) return false;

    // Acting from the menu abandons a half-typed sequence, as any other
    // unrelated input would.
    clearPending();
    run(handler, shortcut.id);
    return true;
  };

  return {
    handleKeyDown,
    invoke,
    getPending: () => pending,
    clearPending,
    destroy: clearPending,
  };
}
