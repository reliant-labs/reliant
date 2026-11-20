/**
 * Write every pending composer draft to storage NOW.
 *
 * The chat composer saves its draft 300ms after the last keystroke
 * (components/Chat/useChatInputState.ts) into workspaceStateStore, which
 * persists to localStorage synchronously. A reload inside that window threw
 * the last keystrokes away. The forced upgrade prompt reloads the tab, so it
 * flushes first (components/AppOutOfDateModal.tsx); `pagehide` flushes too,
 * which covers every other reload and tab close.
 */

type Flush = () => void;

const _pending = new Set<Flush>();
let _listening = false;

function listenForPageHide(): void {
  if (_listening || typeof window === "undefined") return;
  _listening = true;
  window.addEventListener("pagehide", flushPendingDrafts);
}

/** Register a draft owner's flush; returns the function that unregisters it. */
export function registerDraftFlush(flush: Flush): () => void {
  listenForPageHide();
  _pending.add(flush);
  return () => {
    _pending.delete(flush);
  };
}

/** Run every registered flush. One failing never stops the others. */
export function flushPendingDrafts(): void {
  for (const flush of _pending) {
    try {
      flush();
    } catch {
      // A draft that cannot be written is lost either way; the rest still save.
    }
  }
}
