/**
 * Recent client errors, kept on the device for the in-app Diagnostics view.
 *
 * A phone has no devtools. What already exists does not reach the person
 * holding it: the browser-log mirror (lib/browser-log-forward.ts) is DEV ONLY
 * and writes to a file on the dev machine, and ClickStack browser
 * telemetry (lib/telemetry.ts) is consent-gated and land in dashboards the
 * user cannot see. So when a phone shows "something went wrong" there was
 * nothing to look at and nothing to paste into a bug report.
 *
 * This keeps the last MAX_ENTRIES errors in memory, mirrored to
 * sessionStorage so a crash-reload keeps them:
 *
 *   - uncaught errors and unhandled promise rejections (window listeners),
 *   - `console.error` calls (the app logs failed RPCs and stream errors there),
 *   - errors a React error boundary caught (`recordClientError` from
 *     reportCaughtError, before its telemetry consent check: this is local).
 *
 * Local only. Nothing here is sent anywhere; the user copies it out by hand
 * from Settings → Diagnostics. Messages are truncated, never whole payloads.
 *
 * Installed once from lib/browser-log-boot.ts — in every build, unlike the
 * dev mirror beside it, because production phones are the case it is for.
 */

import { useSyncExternalStore } from "react";

export type ClientErrorSource = "uncaught" | "rejection" | "console" | "boundary";

export interface ClientErrorEntry {
  /** ISO-8601. */
  at: string;
  source: ClientErrorSource;
  message: string;
  /** The first lines of the stack, when there is one. */
  stack?: string;
  /** The route it happened on. */
  path: string;
  /** How many identical errors in a row this entry stands for. */
  count: number;
}

export const MAX_ENTRIES = 50;
const MAX_MESSAGE = 500;
const MAX_STACK_LINES = 6;
const STORAGE_KEY = "reliant-client-errors";

let entries: ClientErrorEntry[] = readStored();
const listeners = new Set<() => void>();
let installed = false;
// Recording calls console.error nowhere, but a listener or storage shim might:
// never record an error raised while recording one.
let recording = false;

function readStored(): ClientErrorEntry[] {
  try {
    const raw = globalThis.sessionStorage?.getItem(STORAGE_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? (parsed as ClientErrorEntry[]).slice(-MAX_ENTRIES) : [];
  } catch {
    return [];
  }
}

function persist() {
  try {
    globalThis.sessionStorage?.setItem(STORAGE_KEY, JSON.stringify(entries));
  } catch {
    // Private mode or a full quota: the in-memory copy still serves this tab.
  }
}

function emit() {
  for (const listener of listeners) listener();
}

function describe(value: unknown): { message: string; stack?: string } {
  if (value instanceof Error) {
    return { message: `${value.name}: ${value.message}`, stack: value.stack };
  }
  if (typeof value === "string") return { message: value };
  try {
    return { message: JSON.stringify(value) ?? String(value) };
  } catch {
    return { message: String(value) };
  }
}

function trimStack(stack: string | undefined): string | undefined {
  if (!stack) return undefined;
  return stack.split("\n").slice(0, MAX_STACK_LINES).join("\n");
}

/** Record one error. Consecutive duplicates fold into one entry with a count. */
export function recordClientError(source: ClientErrorSource, ...values: unknown[]): void {
  if (recording) return;
  recording = true;
  try {
    const parts = values.map(describe);
    const message = parts
      .map((part) => part.message)
      .join(" ")
      .slice(0, MAX_MESSAGE);
    if (!message.trim()) return;
    const stack = trimStack(parts.find((part) => part.stack)?.stack);
    const path = typeof location === "undefined" ? "" : location.pathname;
    const last = entries[entries.length - 1];
    if (last && last.message === message && last.source === source && last.path === path) {
      entries = [...entries.slice(0, -1), { ...last, at: new Date().toISOString(), count: last.count + 1 }];
    } else {
      entries = [...entries, { at: new Date().toISOString(), source, message, stack, path, count: 1 }].slice(-MAX_ENTRIES);
    }
    persist();
    emit();
  } finally {
    recording = false;
  }
}

export function getClientErrors(): readonly ClientErrorEntry[] {
  return entries;
}

export function clearClientErrors(): void {
  entries = [];
  persist();
  emit();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** The recorded errors, oldest first; re-renders as new ones arrive. */
export function useClientErrors(): readonly ClientErrorEntry[] {
  return useSyncExternalStore(subscribe, getClientErrors, getClientErrors);
}

/**
 * Start capturing. Idempotent. Wraps `console.error` (after the dev log
 * mirror, if any, so both see every call) and listens for uncaught errors and
 * rejections.
 */
export function installClientDiagnostics(): void {
  if (installed || typeof window === "undefined") return;
  installed = true;

  const originalError = console.error.bind(console);
  console.error = (...args: unknown[]) => {
    recordClientError("console", ...args);
    originalError(...args);
  };

  window.addEventListener("error", (event) => {
    // A failed <script>/<img> load is an Event with no error object.
    recordClientError("uncaught", event.error ?? event.message ?? "Script error");
  });
  window.addEventListener("unhandledrejection", (event) => {
    recordClientError("rejection", event.reason ?? "Unhandled promise rejection");
  });
}

/** Test seam: forget everything, including that it was installed. */
export function resetClientDiagnosticsForTests(): void {
  entries = [];
  installed = false;
  emit();
}
