/**
 * Focus contexts.
 *
 * A context names "where the user is" — which pane or surface has focus. Every
 * shortcut declares the context it belongs to, and at dispatch time only
 * shortcuts whose context is active can fire.
 *
 * Contexts are ordered innermost-first. Resolution walks that order and the
 * first shortcut whose chord matches wins, so an inner context (the Monaco
 * editor) can shadow a binding from an outer one (global) without either side
 * knowing about the other. This replaces the hand-written "is Monaco focused?"
 * branches that used to live in the dispatcher.
 *
 * ── What still belongs OUTSIDE this system ──────────────────────────────────
 *
 * Not every keydown listener is a shortcut, and forcing them all through the
 * registry would make things worse. A direct listener is correct when:
 *
 *   1. It is the semantics of a control, not a command — Enter to send,
 *      Shift+Enter for a newline, xterm's key handling, a dropdown's arrow
 *      navigation. These belong to the component that owns the control.
 *   2. It lives on a route where the dispatcher is not mounted. ModernApp
 *      renders only on `/` and `/project/$projectId`, so the workflow builder
 *      (`/workflow/*`) has no global handler to race.
 *   3. Its trigger condition is weaker than any focus context — e.g. FileTree's
 *      PageUp/PageDown, which must work while the tree is visible but unfocused.
 *
 * The rule that keeps this honest: a direct listener must not claim a chord
 * that config/shortcuts.yaml binds. `bindings.test.ts` enforces the inverse —
 * no shortcut may claim a chord these listeners own — so the two sets stay
 * disjoint and neither can silently swallow the other.
 */

export const CONTEXTS = [
  // Innermost: transient surfaces that fully capture input while open.
  "modal",
  "slash-menu",
  // Editing surfaces.
  "monaco",
  "terminal",
  "chat-input",
  // Structural panes.
  "file-tree",
  "workflow-canvas",
  "right-sidebar",
  // Outermost: always active, lowest precedence.
  "global",
] as const;

export type ShortcutContext = (typeof CONTEXTS)[number];

/** Precedence index — lower means more specific, and wins. */
const CONTEXT_RANK = new Map<string, number>(
  CONTEXTS.map((context, index) => [context, index]),
);

export function contextRank(context: string): number {
  return CONTEXT_RANK.get(context) ?? CONTEXTS.length;
}

/** Sort contexts innermost-first. */
export function sortByPrecedence(contexts: readonly string[]): string[] {
  return [...contexts].sort((a, b) => contextRank(a) - contextRank(b));
}

/**
 * Contexts that represent a text-entry surface.
 *
 * A shortcut without modifiers would otherwise swallow ordinary typing, so
 * bare-key bindings are suppressed while one of these is active unless the
 * shortcut opts in via `allowInInput`.
 */
export const TEXT_ENTRY_CONTEXTS: ReadonlySet<string> = new Set([
  "chat-input",
  "terminal",
  "monaco",
]);

export function isTextEntryContext(context: string): boolean {
  return TEXT_ENTRY_CONTEXTS.has(context);
}

/**
 * Detect the contexts currently active from the DOM.
 *
 * This is deliberately DOM-derived rather than React state: focus moves through
 * portals, xterm's hidden textarea, and Monaco's internal inputs, none of which
 * reliably round-trip through a store. `global` is always included as the
 * fallback.
 */
export function detectActiveContexts(
  target: Element | null = typeof document !== "undefined"
    ? document.activeElement
    : null,
): ShortcutContext[] {
  const active: ShortcutContext[] = [];

  if (typeof document !== "undefined") {
    // Modal state is a document-level fact, not a focus fact: a modal can be
    // open while focus is still settling into it.
    if (document.querySelector('[data-modal-open="true"]')) {
      active.push("modal");
    }
    if (document.querySelector('[data-slash-menu-open="true"]')) {
      active.push("slash-menu");
    }
  }

  if (target) {
    if (target.closest(".monaco-editor")) active.push("monaco");
    if (target.closest(".xterm")) active.push("terminal");
    if (target.closest('[data-context="chat-input"]')) active.push("chat-input");
    if (target.closest(".file-tree-container")) active.push("file-tree");
    if (target.closest('[data-context="workflow-canvas"]')) {
      active.push("workflow-canvas");
    }
    if (target.closest('[data-context="right-sidebar"]')) {
      active.push("right-sidebar");
    }
  }

  active.push("global");
  return sortByPrecedence(active) as ShortcutContext[];
}

/**
 * True when focus is in a raw text-entry element.
 *
 * Used to suppress bare-key shortcuts even in contexts we do not explicitly
 * model — a search box inside a settings panel is still typing.
 */
export function isEditableTarget(target: Element | null): boolean {
  if (!target) return false;
  const el = target as HTMLElement;
  return (
    el.tagName === "INPUT" ||
    el.tagName === "TEXTAREA" ||
    el.isContentEditable ||
    el.closest('input, textarea, [contenteditable="true"]') !== null
  );
}
