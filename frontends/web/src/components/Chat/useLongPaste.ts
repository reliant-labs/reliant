import { useEffect, useRef, type RefObject } from "react";

/** A paste at or past either threshold arrives as a text attachment, not inline text. */
export const LONG_PASTE_MIN_CHARACTERS = 5000;
export const LONG_PASTE_MIN_LINES = 100;

export function isLongPaste(text: string): boolean {
  if (text.length >= LONG_PASTE_MIN_CHARACTERS * 2) return true; // every code point is at most two units
  return (
    Array.from(text).length >= LONG_PASTE_MIN_CHARACTERS ||
    text.split(/\r\n|\r|\n/).length >= LONG_PASTE_MIN_LINES
  );
}

/** `pasted-text.txt`, then `pasted-text(2).txt`, `pasted-text(3).txt`… — the first not in `taken`. */
export function pastedTextFilename(taken: Iterable<string>): string {
  const names = new Set(taken);
  if (!names.has("pasted-text.txt")) return "pasted-text.txt";
  for (let n = 2; ; n++) {
    const name = `pasted-text(${n}).txt`;
    if (!names.has(name)) return name;
  }
}

/**
 * v1.10 kept each long paste's raw text in localStorage, under
 * `reliant:long-paste:v1:` + JSON [user id, project id, attachment session],
 * and showed it in a panel of its own. Draft attachments now persist
 * themselves, so a stored paste is uploaded once as an ordinary draft
 * attachment and its entry dropped — only after its upload succeeds, so a
 * failure leaves it for the next mount. Remove with the next major.
 */
const LEGACY_PASTE_PREFIX = "reliant:long-paste:v1:";
const migrating = new Set<string>();

export function legacyPasteDraftKey(userId: string | undefined, projectId: string | undefined, sessionId: string): string {
  return LEGACY_PASTE_PREFIX + JSON.stringify([userId, projectId, sessionId]);
}

export async function migrateLegacyPasteDrafts(keys: string[], attach: (text: string) => Promise<unknown>): Promise<void> {
  for (const key of keys) {
    if (migrating.has(key)) continue;
    let rows: Array<{ id: unknown; text: unknown }>;
    try {
      const raw = localStorage.getItem(key);
      if (!raw) continue;
      const parsed: unknown = JSON.parse(raw);
      rows = Array.isArray(parsed) ? parsed : [];
    } catch {
      continue;
    }
    migrating.add(key);
    let remaining = rows;
    const save = () => {
      try {
        if (remaining.length) localStorage.setItem(key, JSON.stringify(remaining));
        else localStorage.removeItem(key);
      } catch {
        // Storage refused the write; the next mount sees the entry again.
      }
    };
    try {
      for (const row of rows) {
        if (typeof row?.text === "string" && row.text !== "") await attach(row.text);
        remaining = remaining.filter((r) => r !== row);
        save();
      }
    } catch {
      // An upload failed (attachFile already raised a toast); the rest wait for the next mount.
    } finally {
      migrating.delete(key);
    }
  }
}

/**
 * Turn a long paste into the composer textarea into a text attachment.
 *
 * Cmd/Ctrl+Shift+V pastes inline whatever its length (the browser's "paste
 * as plain text"). Electron's "Paste as Plain Text" menu item, which owns that
 * accelerator there, delivers the clipboard as a `reliant:paste-plain-text`
 * event instead of a paste, and is inserted inline the same way.
 */
export function useLongPaste({ textarea, input, setInput, attach, disabled }: {
  textarea: RefObject<HTMLTextAreaElement | null>;
  input: string;
  setInput: (text: string) => void;
  /** Upload `text` as an attachment. */
  attach: (text: string) => void;
  disabled: boolean;
}) {
  const bypass = useRef(false);
  useEffect(() => {
    const el = textarea.current;
    if (!el) return;
    const key = (e: KeyboardEvent) => {
      bypass.current = (e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "v";
    };
    const reset = () => { bypass.current = false; };
    const paste = (e: ClipboardEvent) => {
      const skip = bypass.current;
      bypass.current = false;
      if (disabled || skip || e.defaultPrevented || e.target !== el || !e.clipboardData) return;
      if (e.clipboardData.files.length || Array.from(e.clipboardData.items).some((item) => item.kind === "file")) return;
      const text = e.clipboardData.getData("text/plain");
      if (!isLongPaste(text)) return;
      e.preventDefault();
      // A paste replaces the selection, whichever form the text arrives in.
      if (el.selectionStart !== el.selectionEnd) setInput(input.slice(0, el.selectionStart) + input.slice(el.selectionEnd));
      attach(text);
    };
    const plain = (event: Event) => {
      if (event.target !== el || disabled) return;
      const text = (event as CustomEvent<{ text: string }>).detail?.text;
      if (typeof text !== "string") return;
      event.preventDefault();
      setInput(input.slice(0, el.selectionStart) + text + input.slice(el.selectionEnd));
      bypass.current = false;
    };
    el.addEventListener("reliant:paste-plain-text", plain);
    el.addEventListener("keydown", key, true);
    el.addEventListener("paste", paste);
    el.addEventListener("blur", reset);
    el.addEventListener("keyup", reset);
    return () => {
      el.removeEventListener("reliant:paste-plain-text", plain);
      el.removeEventListener("keydown", key, true);
      el.removeEventListener("paste", paste);
      el.removeEventListener("blur", reset);
      el.removeEventListener("keyup", reset);
    };
  });
}
