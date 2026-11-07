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
 * attachment. Its entry is dropped only once the new attachment is in the
 * PERSISTED draft — an upload that fails, or a draft write storage refuses,
 * leaves the text for the next mount. Remove with the next major.
 */
const LEGACY_PASTE_PREFIX = "reliant:long-paste:v1:";
const migrating = new Set<string>();
// Rows uploaded on this page load whose draft write was not yet confirmed: a
// later mount re-checks the same attachment rather than uploading the text again.
const uploadedRows = new Map<string, string>();

export function legacyPasteDraftKey(userId: string | undefined, projectId: string | undefined, sessionId: string): string {
  return LEGACY_PASTE_PREFIX + JSON.stringify([userId, projectId, sessionId]);
}

/**
 * The stored v1.10 pastes that belong to THIS draft: the signed-in user's, in
 * the current project, for this attachment session — plus, for a new chat,
 * the same user's and project's old desktop "temp" slot. The user and project
 * are part of every key, so another account's or project's pastes are never
 * read; with either unknown, none are.
 *
 * v1.10's "temp" was never a global slot: its ChatInput keyed the paste store
 * by JSON [user id, current project id, attachment session], so a paste made
 * in a new chat in p1 lives under [u, "p1", "temp"] and only a new chat in p1
 * reads it back.
 */
export function legacyPasteDraftKeys({ userId, projectId, sessionId, newChat }: {
  userId: string | undefined;
  projectId: string | undefined;
  sessionId: string;
  newChat: boolean;
}): string[] {
  if (!userId || !projectId) return [];
  const keys = [legacyPasteDraftKey(userId, projectId, sessionId)];
  // v1.10's desktop new chat drafted into "temp"; it drafts per project now.
  if (newChat && sessionId !== "temp") keys.push(legacyPasteDraftKey(userId, projectId, "temp"));
  return keys;
}

/**
 * @param attach uploads `text` as a draft attachment and resolves its id.
 * @param persisted whether the draft attachment `id` survives a reload.
 */
export async function migrateLegacyPasteDrafts(
  keys: string[],
  attach: (text: string) => Promise<string>,
  persisted: (id: string) => boolean,
): Promise<void> {
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
        if (typeof row?.text === "string" && row.text !== "") {
          const rowKey = `${key}\u0000${JSON.stringify(row.id)}\u0000${row.text.length}`;
          let id = uploadedRows.get(rowKey);
          if (id === undefined) {
            id = await attach(row.text);
            uploadedRows.set(rowKey, id);
          }
          // The legacy text is the only copy that survives a reload until then.
          if (!persisted(id)) break;
          uploadedRows.delete(rowKey);
        }
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
