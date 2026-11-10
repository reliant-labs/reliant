import { createContext, createElement, useContext, useEffect, useSyncExternalStore, type ReactNode } from "react";

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
// Synthetic readers belong to an isolated fixture subtree, never the global auth store.
// Only createFixtureReader mints one (a fresh object, never `current`), so an
// authenticated session can never become a fixture reader and outlive its epoch.
const mintedFixtureReaders = new WeakSet<ReaderSession>();
const mountedFixtureReaders = new WeakSet<ReaderSession>();
const ReaderContext = createContext<ReaderSession | null>(null);
export function FixtureReaderProvider({ reader, children }: { reader: ReaderSession; children: ReactNode }) {
  if (!mintedFixtureReaders.has(reader)) throw new Error("FixtureReaderProvider takes only a reader from createFixtureReader");
  useEffect(() => { mountedFixtureReaders.add(reader); return () => { mountedFixtureReaders.delete(reader); }; }, [reader]);
  return createElement(ReaderContext.Provider, { value: reader }, children);
}
export function createFixtureReader(): ReaderSession {
  const reader: ReaderSession = Object.freeze({ principalId: "synthetic-console-demo", epoch: crypto.randomUUID() });
  mintedFixtureReaders.add(reader);
  mountedFixtureReaders.add(reader);
  return reader;
}
/** The authenticated path: the captured reader IS the current session, nothing else. */
const isAuthenticatedReader = (reader: ReaderSession) => !!reader.principalId && reader === current;
/** The synthetic path: a reader createFixtureReader minted, still mounted. */
const isFixtureReader = (reader: ReaderSession) => mintedFixtureReaders.has(reader) && mountedFixtureReaders.has(reader);
export const isCurrentReader = (reader: ReaderSession) => isAuthenticatedReader(reader) || isFixtureReader(reader);
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
  const fixture = useContext(ReaderContext);
  const authenticated = useSyncExternalStore(subscribeReaderSession, getReaderSession, getReaderSession);
  return fixture ?? authenticated;
}
/** Reject before issuing a request AND after awaiting it, even if it ignored abort. */
export function requireCurrentReader(reader: ReaderSession): void {
  if (!isCurrentReader(reader)) throw new DOMException("Reader session changed", "AbortError");
}
export const readerStorageScope = (reader: ReaderSession) => `${encodeURIComponent(reader.principalId ?? "signed-out")}.${reader.epoch}`;
