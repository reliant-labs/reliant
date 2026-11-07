// Copyright (c) 2025 Reliant Labs

/**
 * What the demo remembers for one browser session (sessionStorage, so a new
 * tab or a new visit starts fresh): whether the tour toast was dismissed,
 * which views' tips were shown, and whether tips were turned off. Nothing
 * else is stored, and nothing leaves the browser.
 *
 * Read in React with `useDemoSession()`; a test resets it with
 * `resetDemoSessionForTests()`.
 */

import { useSyncExternalStore } from "react";

import type { DemoTipKey } from "./tips/tipKeys";

const KEY = "forge-demo.session.v1";

export interface DemoSessionState {
  toastDismissed: boolean;
  tipsDisabled: boolean;
  seenTips: readonly DemoTipKey[];
}

const EMPTY: DemoSessionState = { toastDismissed: false, tipsDisabled: false, seenTips: [] };

function storage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.sessionStorage;
  } catch {
    return null;
  }
}

function read(): DemoSessionState {
  try {
    const raw = storage()?.getItem(KEY);
    if (!raw) return EMPTY;
    const parsed = JSON.parse(raw) as Partial<DemoSessionState>;
    return {
      toastDismissed: parsed.toastDismissed === true,
      tipsDisabled: parsed.tipsDisabled === true,
      seenTips: Array.isArray(parsed.seenTips) ? (parsed.seenTips.filter((k) => typeof k === "string") as DemoTipKey[]) : [],
    };
  } catch {
    return EMPTY;
  }
}

let state: DemoSessionState | null = null;
const listeners = new Set<() => void>();

function current(): DemoSessionState {
  return (state ??= read());
}

function update(patch: Partial<DemoSessionState>): void {
  state = { ...current(), ...patch };
  try {
    storage()?.setItem(KEY, JSON.stringify(state));
  } catch {
    // Storage full or blocked: this page load still remembers.
  }
  listeners.forEach((fn) => fn());
}

export const demoSession = {
  get: current,
  dismissToast: () => !current().toastDismissed && update({ toastDismissed: true }),
  disableTips: () => update({ tipsDisabled: true }),
  hasSeenTip: (key: DemoTipKey) => current().seenTips.includes(key),
  markTipSeen: (key: DemoTipKey) => !current().seenTips.includes(key) && update({ seenTips: [...current().seenTips, key] }),
  subscribe: (fn: () => void) => {
    listeners.add(fn);
    return () => void listeners.delete(fn);
  },
};

export function useDemoSession(): DemoSessionState {
  return useSyncExternalStore(demoSession.subscribe, current, current);
}

/** Test seam: forget this session, in memory and in sessionStorage. */
export function resetDemoSessionForTests(): void {
  state = null;
  try {
    storage()?.removeItem(KEY);
  } catch {
    // Nothing to forget.
  }
  listeners.forEach((fn) => fn());
}
