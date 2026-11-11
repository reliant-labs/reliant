// Copyright (c) 2025 Reliant Labs

/**
 * Whether the nav's Observability group is expanded: open by default on every
 * page, and remembered once the reader collapses (or reopens) it. One
 * preference for both consoles, like the rail: collapsing it in an app keeps
 * it collapsed in the org console too.
 */

import { useCallback, useState } from "react";

export const OBS_NAV_OPEN_KEY = "forge.console.nav.observability";

export function readObsNavOpen(): boolean {
  try {
    return window.localStorage.getItem(OBS_NAV_OPEN_KEY) !== "0";
  } catch {
    return true;
  }
}

function writeObsNavOpen(open: boolean): void {
  try {
    window.localStorage.setItem(OBS_NAV_OPEN_KEY, open ? "1" : "0");
  } catch {
    // Not persisted; the group still toggles for this visit.
  }
}

/** The group's open state and its setter, persisted. */
export function useObsNavOpen(): [boolean, (open: boolean) => void] {
  const [open, setOpenState] = useState(readObsNavOpen);
  const setOpen = useCallback((next: boolean) => {
    writeObsNavOpen(next);
    setOpenState(next);
  }, []);
  return [open, setOpen];
}
