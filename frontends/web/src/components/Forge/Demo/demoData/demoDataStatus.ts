// Copyright (c) 2025 Reliant Labs

/**
 * Where the demo console's reads are coming from right now, for its banner:
 *
 *   fixtures   the built-in sample data (the default)
 *   live       the demo-data gateway (DEMO_DATA_SOURCE=remote), answering
 *   fallback   the gateway failed at least once this page load, and the
 *              sample data answered instead
 *
 * "fallback" sticks for the page load: a banner that flickers between live and
 * sample data on every slow read would be worse than one that says so once.
 */

import { useSyncExternalStore } from "react";

export type DemoDataStatus = "fixtures" | "live" | "fallback";

let status: DemoDataStatus = "fixtures";
const listeners = new Set<() => void>();

export function setDemoDataStatus(next: DemoDataStatus): void {
  if (status === next || (status === "fallback" && next === "live")) return;
  status = next;
  for (const listener of listeners) listener();
}

export function getDemoDataStatus(): DemoDataStatus {
  return status;
}

export function useDemoDataStatus(): DemoDataStatus {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => status,
    () => status,
  );
}

/** Test seam. */
export function resetDemoDataStatusForTests(): void {
  status = "fixtures";
}
