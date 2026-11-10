import { useSyncExternalStore } from "react";

/** No credentials or PII: an authenticated reader and one in-memory auth epoch.
 * Reload deliberately starts a fresh epoch and asks for confirmation again. */
export interface ReaderSession { readonly principalId: string | null; readonly epoch: string }
let current: ReaderSession = { principalId: null, epoch: crypto.randomUUID() };
const listeners = new Set<() => void>();
const LEGACY = /^forge\.(storage|data)\.(access|opened)\./;

function discardConfirmations() {
  try {
    for (const key of Object.keys(window.sessionStorage)) {
      if (LEGACY.test(key)) window.sessionStorage.removeItem(key);
    }
  } catch { /* Storage may be unavailable; the epoch still invalidates memory. */ }
}

// A reload has no confirmed reader yet; discard all older epochs and legacy keys.
discardConfirmations();
export const getReaderSession = () => current;
export const isCurrentReader = (reader: ReaderSession) => !!reader.principalId && reader === current;
export function setReaderPrincipal(principalId: string | null, force = false): void {
  if (!force && current.principalId === principalId) return;
  discardConfirmations(); // Never migrate a legacy/unscoped reason into a new identity.
  current = { principalId, epoch: crypto.randomUUID() };
  for (const listener of listeners) listener();
}
export function subscribeReaderSession(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}
export function useReaderSession(): ReaderSession {
  return useSyncExternalStore(subscribeReaderSession, getReaderSession, getReaderSession);
}
/** Reject before issuing a request AND after awaiting it, even if it ignored abort. */
export function requireCurrentReader(reader: ReaderSession): void {
  if (!isCurrentReader(reader)) throw new DOMException("Reader session changed", "AbortError");
}
export const readerStorageScope = (reader: ReaderSession) => `${encodeURIComponent(reader.principalId ?? "signed-out")}.${reader.epoch}`;
