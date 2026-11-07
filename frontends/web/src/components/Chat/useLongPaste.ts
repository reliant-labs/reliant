import { useEffect, useRef, useState, type RefObject } from "react";
import type { useComposerUploads } from "./useComposerUploads";

type Preferences = { enabled: boolean; characterThreshold: number; lineThreshold: number };
export const pasteCounts = (text: string) => ({ characters: Array.from(text).length, lines: text.split(/\r\n|\r|\n/).length });
export function isLongPaste(text: string, prefs: Preferences) {
  const counts = pasteCounts(text);
  return prefs.enabled && (counts.characters >= prefs.characterThreshold || counts.lines >= prefs.lineThreshold);
}
type Paste = { id: number; text: string; upload?: number };
export const pastedFilename = (id: number) => `Pasted text ${id}.txt`;
const PREFIX = "reliant:long-paste:v1:";
// Small enough for synchronous browser draft storage; larger pastes stay inline.
const MAX_DRAFT_BYTES = 1024 * 1024;
function read(session: string): Paste[] {
  try {
    const raw = localStorage.getItem(PREFIX + session) || "[]";
    if (new Blob([raw]).size > MAX_DRAFT_BYTES) return [];
    const value: unknown = JSON.parse(raw);
    const seen = new Set<number>();
    return Array.isArray(value) ? value.filter((v) => {
      if (typeof v?.text !== "string" || !Number.isSafeInteger(v?.id) || seen.has(v.id)) return false;
      seen.add(v.id); return true;
    }).map(({ id, text }) => ({ id, text })) : [];
  } catch { return []; }
}
function persist(session: string, rows: Paste[]) {
  const value = JSON.stringify(rows.map(({ id, text }) => ({ id, text })));
  if (new Blob([value]).size > MAX_DRAFT_BYTES) return false;
  try {
    if (rows.length) localStorage.setItem(PREFIX + session, value);
    else localStorage.removeItem(PREFIX + session);
    return true;
  } catch { return false; }
}

export function useLongPaste({ session, textarea, input, setInput, preferences, queue, disabled }: {
  session: string; textarea: RefObject<HTMLTextAreaElement | null>; input: string;
  setInput: (text: string) => void; preferences: Preferences;
  queue: ReturnType<typeof useComposerUploads>; disabled: boolean;
}) {
  const drafts = useRef(new Map<string, Paste[]>());
  if (!drafts.current.has(session)) drafts.current.set(session, read(session));
  const [, render] = useState(0);
  const [notice, setNotice] = useState("");
  const undo = useRef<{ session: string; id: number; before: string; after: string; start: number } | null>(null);
  const bypass = useRef(false);
  const rows = drafts.current.get(session)!;
  const update = (next: Paste[]) => { drafts.current.set(session, next); persist(session, next); render((v) => v + 1); };
  const remove = (id: number) => {
    const row = rows.find((r) => r.id === id);
    if (row?.upload !== undefined) queue.discard(row.upload);
    update(rows.filter((r) => r.id !== id));
    undo.current = null;
  };
  const insert = (id: number) => {
    const row = rows.find((r) => r.id === id);
    if (!row) return;
    const el = textarea.current;
    const start = el?.selectionStart ?? input.length;
    const end = el?.selectionEnd ?? start;
    setInput(input.slice(0, start) + row.text + input.slice(end));
    remove(id);
    el?.focus();
  };
  useEffect(() => {
    const el = textarea.current;
    if (!el) return;
    const key = (e: KeyboardEvent) => {
      bypass.current = (e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "v";
      const last = undo.current;
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && e.key.toLowerCase() === "z" && last?.session === session && input === last.after) {
        const row = rows.find((r) => r.id === last.id);
        if (row) {
          e.preventDefault(); e.stopPropagation();
          setInput(last.before.slice(0, last.start) + row.text + last.before.slice(last.start + (last.before.length - last.after.length)));
          remove(row.id);
        }
      } else if (!bypass.current && !["Meta", "Control", "Shift"].includes(e.key)) undo.current = null;
    };
    const paste = (e: ClipboardEvent) => {
      const skip = bypass.current; bypass.current = false;
      if (disabled || skip || e.defaultPrevented || e.target !== el || !e.clipboardData) return;
      if (e.clipboardData.files.length || Array.from(e.clipboardData.items).some((item) => item.kind === "file")) return;
      const text = e.clipboardData.getData("text/plain");
      if (!isLongPaste(text, preferences)) return;
      const id = Math.max(Date.now(), ...rows.map((r) => r.id + 1));
      const row: Paste = { id, text };
      if (!persist(session, [...rows, row])) {
        setNotice("Paste kept as text: attachment draft storage is full or unavailable.");
        return;
      }
      e.preventDefault();
      const start = el.selectionStart;
      const after = input.slice(0, start) + input.slice(el.selectionEnd);
      row.upload = queue.enqueue(new File([text], pastedFilename(id), { type: "text/plain" }));
      update([...rows, row]);
      setInput(after);
      undo.current = { session, id, before: input, after, start };
      setNotice("Long paste attached. Undo inserts it as text; Shift+paste bypasses conversion.");
    };
    const blur = () => { bypass.current = false; };
    const plain = (event: Event) => {
      if (event.target !== el || disabled) return;
      const text = (event as CustomEvent<{ text: string }>).detail?.text;
      if (typeof text !== "string") return;
      event.preventDefault();
      setInput(input.slice(0, el.selectionStart) + text + input.slice(el.selectionEnd));
      undo.current = null;
      bypass.current = false;
    };
    el.addEventListener("reliant:paste-plain-text", plain);
    el.addEventListener("keydown", key, true);
    el.addEventListener("paste", paste);
    el.addEventListener("blur", blur);
    el.addEventListener("keyup", blur);
    return () => { el.removeEventListener("reliant:paste-plain-text", plain); el.removeEventListener("keydown", key, true); el.removeEventListener("paste", paste); el.removeEventListener("blur", blur); el.removeEventListener("keyup", blur); };
  });
  return {
    rows, notice, remove, insert,
    status: (id: number) => {
      const row = rows.find((r) => r.id === id);
      return queue.uploads.find((u) => u.id === row?.upload)?.status ?? (row?.upload === undefined ? "restored" : "ready");
    },
    needsRecovery: rows.some((r) => r.upload === undefined),
    retry: (id: number) => {
      const row = rows.find((r) => r.id === id);
      if (!row) return;
      if (!row.text.length) { setNotice("Empty pasted text cannot be uploaded. Insert as text or remove it."); return; }
      if (row.upload !== undefined) void queue.retry(row.upload);
      else update(rows.map((r) => r.id === id ? { ...r, upload: queue.enqueue(new File([r.text], pastedFilename(r.id), { type: "text/plain" })) } : r));
    },
    edit: (id: number, text: string) => {
      const next = rows.map((r) => r.id === id ? { id, text } : r);
      if (!persist(session, next)) { setNotice("Edit not saved: draft storage is full or unavailable. Original text retained."); return false; }
      const row = rows.find((r) => r.id === id);
      if (row?.upload !== undefined) queue.discard(row.upload);
      update(next);
      undo.current = null;
      return true;
    },
    submitting: () => {
      rows.forEach((row) => { if (row.upload !== undefined) queue.transferToSend(row.upload); });
    },
    sendFailed: () => {
      const ids = new Set(rows.map((r) => r.id));
      const next = (drafts.current.get(session) ?? []).map((r) => ids.has(r.id) ? { id: r.id, text: r.text } : r);
      drafts.current.set(session, next); persist(session, next); render((v) => v + 1);
    },
    // Called only after the existing send/queue path has accepted this draft.
    sent: () => {
      const remaining = (drafts.current.get(session) ?? []).filter((row) => !rows.includes(row));
      drafts.current.set(session, remaining); persist(session, remaining);
      undo.current = null;
      rows.forEach((row) => { if (row.upload !== undefined) queue.forgetCompleted(row.upload); });
      render((v) => v + 1);
    },
  };
}
