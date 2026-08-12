/**
 * Pane focus.
 *
 * A small vocabulary for "put my cursor over there", so the keyboard can move
 * between the panes of the app the way the mouse does. Every pane resolves
 * through one function, which means the rules — reveal a hidden pane before
 * focusing it, remember where focus came from, restore it on the way out — are
 * written once instead of per shortcut.
 *
 * Panes are addressed by name rather than by element because most of them are
 * conditionally rendered and several own their own focus behavior (xterm has a
 * hidden textarea, Monaco has an internal input, the file tree has an
 * imperative handle). Each pane advertises how to focus itself.
 */

export const PANES = [
  "chat-input",
  "transcript",
  "editor",
  "terminal",
  "left-sidebar",
  "right-sidebar",
] as const;

export type Pane = (typeof PANES)[number];

/**
 * Where focus was before the current pane took it.
 *
 * Used to make focus moves reversible: closing or leaving a pane returns the
 * cursor to where the user actually was, rather than dropping it on the body
 * where the next keystroke goes nowhere.
 */
let previousPane: Pane | null = null;
let currentPane: Pane | null = null;

/** Event a pane listens for to focus itself. */
export function focusEventName(pane: Pane): string {
  return `focus-pane:${pane}`;
}

/**
 * Ask a pane to take focus.
 *
 * Records the move so `focusPrevious` can undo it. The pane itself decides what
 * "focused" means; panes that are hidden are expected to reveal themselves
 * first (the shortcut handler does this before dispatching).
 */
export function focusPane(pane: Pane): void {
  if (currentPane !== pane) {
    previousPane = currentPane;
    currentPane = pane;
  }
  window.dispatchEvent(new CustomEvent(focusEventName(pane)));
}

/**
 * Return focus to the pane it came from, defaulting to the composer.
 *
 * The composer is the fallback because it is the one pane that is always
 * present and always accepts typing — landing there is never a dead end.
 */
export function focusPrevious(fallback: Pane = "chat-input"): void {
  const target = previousPane ?? fallback;
  previousPane = null;
  currentPane = target;
  window.dispatchEvent(new CustomEvent(focusEventName(target)));
}

/** The pane focus was last moved to, if any. */
export function getCurrentPane(): Pane | null {
  return currentPane;
}

/**
 * Note that focus left the tracked panes.
 *
 * Called when a pane is closed or focus moves somewhere we do not model, so a
 * later `focusPrevious` does not send the user back into a hidden pane.
 */
export function clearPane(pane: Pane): void {
  if (currentPane === pane) currentPane = previousPane;
  if (previousPane === pane) previousPane = null;
}

/**
 * True when focus currently sits inside the given pane.
 *
 * Read from the DOM rather than the tracked state: the user can click into a
 * pane, and a shortcut that closes "the pane I am in" has to respect that.
 */
export function isFocusWithin(pane: Pane): boolean {
  if (typeof document === "undefined") return false;
  const active = document.activeElement;
  if (!active) return false;

  switch (pane) {
    case "chat-input":
      return active.closest('[data-context="chat-input"]') !== null;
    case "terminal":
      return active.closest(".xterm") !== null;
    case "editor":
      return active.closest(".monaco-editor") !== null;
    case "right-sidebar":
      return active.closest('[data-context="right-sidebar"]') !== null;
    case "left-sidebar":
      return active.closest('[data-context="left-sidebar"]') !== null;
    case "transcript":
      return active.closest('[data-context="transcript"]') !== null;
  }
}
